/*
 * ChatCLI - Long-term memory tests: explicit note links (links.json).
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/atrest"
	"go.uber.org/zap"
)

func TestLinkStore_AddRemoveAndPersist(t *testing.T) {
	dir := t.TempDir()
	ls := NewLinkStore(dir, zap.NewNop())
	if !addLink(ls, "fact:a", "project:x", "Explains The", "", "  why  this\nmatters ") {
		t.Fatal("add failed")
	}
	if addLink(ls, "fact:a", "project:x", "explains_the", "user", "why this matters") {
		t.Fatal("identical re-add must be a no-op")
	}
	all := ls.All()
	if len(all) != 1 || all[0].Rel != "explains_the" || all[0].Origin != "user" || all[0].Note != "why this matters" {
		t.Fatalf("stored = %+v", all)
	}
	// Survives a restart.
	if got := NewLinkStore(dir, zap.NewNop()).All(); len(got) != 1 {
		t.Fatalf("reloaded = %+v", got)
	}
	// Remove is direction-agnostic.
	if n := ls.Remove("project:x", "fact:a", ""); n != 1 {
		t.Fatalf("removed %d", n)
	}
	if ls.Count() != 0 || len(NewLinkStore(dir, zap.NewNop()).All()) != 0 {
		t.Fatal("removal not persisted")
	}
	// Revive after removal.
	if !addLink(ls, "fact:a", "project:x", "explains_the", "user", "") || ls.Count() != 1 {
		t.Fatal("re-adding a removed link must revive it")
	}
}

// addLink is LinkStore.Add reduced to "did the store change".
func addLink(ls *LinkStore, from, to, rel, origin, note string) bool {
	changed, _ := ls.Add(from, to, rel, origin, note)
	return changed
}

func TestLinkStore_AddReportsWhy(t *testing.T) {
	ls := NewLinkStore(t.TempDir(), zap.NewNop())
	if _, err := ls.Add("a", "a", "", "", ""); !errors.Is(err, ErrLinkInvalid) {
		t.Fatalf("self link err = %v", err)
	}
	if changed, err := ls.Add("a", "b", "", "", ""); !changed || err != nil {
		t.Fatalf("first add = %v %v", changed, err)
	}
	if changed, err := ls.Add("a", "b", "", "", ""); changed || err != nil {
		t.Fatalf("duplicate add must be (false, nil), got %v %v", changed, err)
	}
}

func TestLinkStore_RemapMovesEndpoints(t *testing.T) {
	ls := NewLinkStore(t.TempDir(), zap.NewNop())
	addLink(ls, "fact:old", "project:x", "about", "user", "why")
	addLink(ls, "topic:t", "fact:old", "related", "user", "")
	addLink(ls, "fact:old", "fact:new", "related", "user", "") // collapses
	if n := ls.Remap(map[string]string{"fact:old": "fact:new"}); n != 2 {
		t.Fatalf("moved %d, want 2", n)
	}
	got := map[string]StoredLink{}
	for _, l := range ls.All() {
		got[l.From+">"+l.To] = l
	}
	if l, ok := got["fact:new>project:x"]; !ok || l.Rel != "about" || l.Note != "why" {
		t.Fatalf("remapped link lost its data: %+v", got)
	}
	if _, ok := got["topic:t>fact:new"]; !ok || len(got) != 2 {
		t.Fatalf("links after remap = %+v", got)
	}
}

func TestLinkStore_ReadsSeeOtherProcessWrites(t *testing.T) {
	dir := t.TempDir()
	reader := NewLinkStore(dir, zap.NewNop())
	if reader.Count() != 0 {
		t.Fatal("expected empty")
	}
	time.Sleep(5 * time.Millisecond)
	addLink(NewLinkStore(dir, zap.NewNop()), "fact:a", "topic:b", "", "", "")
	if reader.Count() != 1 || len(reader.All()) != 1 {
		t.Fatal("a link written by another process is invisible to this one")
	}
}

func TestLinkStore_RejectsInvalid(t *testing.T) {
	ls := NewLinkStore(t.TempDir(), zap.NewNop())
	for _, c := range [][2]string{{"", "b"}, {"a", ""}, {"a", "a"}, {strings.Repeat("x", maxLinkEndpointBytes+1), "b"}} {
		if addLink(ls, c[0], c[1], "", "", "") {
			t.Fatalf("accepted invalid link %q → %q", c[0], c[1])
		}
	}
	if ls.Count() != 0 {
		t.Fatal("invalid link stored")
	}
}

func TestNormalizeRel(t *testing.T) {
	cases := map[string]string{
		"":                 "related",
		"  ":               "related",
		"Depends On":       "depends_on",
		"explica-o_tópico": "explica_o_topico",
		"!!!":              "related",
		"a--b":             "a_b",
	}
	for in, want := range cases {
		if got := NormalizeRel(in); got != want {
			t.Errorf("NormalizeRel(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NormalizeRel(strings.Repeat("abc ", 40)); len(got) > maxLinkRelBytes+1 {
		t.Errorf("rel not bounded: %d bytes", len(got))
	}
}

// Two processes share links.json: one removes a link the other still holds
// in memory. The other's next write must NOT resurrect it (tombstone LWW).
func TestLinkStore_MultiProcessRemovalIsNotResurrected(t *testing.T) {
	dir := t.TempDir()
	p1 := NewLinkStore(dir, zap.NewNop())
	addLink(p1, "fact:a", "topic:t", "", "", "")
	p2 := NewLinkStore(dir, zap.NewNop()) // loads the link
	time.Sleep(2 * time.Millisecond)
	if p1.Remove("fact:a", "topic:t", "") != 1 {
		t.Fatal("remove failed")
	}
	time.Sleep(2 * time.Millisecond)
	addLink(p2, "fact:b", "topic:t", "", "", "") // p2 writes, merging disk
	final := NewLinkStore(dir, zap.NewNop()).All()
	if len(final) != 1 || final[0].From != "fact:b" {
		t.Fatalf("removed link resurrected or new link lost: %+v", final)
	}
	// And p2 itself sees the removal after its merge.
	for _, l := range p2.All() {
		if l.From == "fact:a" {
			t.Fatal("p2 still serves the removed link")
		}
	}
}

func TestLinkStore_MultiProcessAddsMerge(t *testing.T) {
	dir := t.TempDir()
	p1 := NewLinkStore(dir, zap.NewNop())
	p2 := NewLinkStore(dir, zap.NewNop())
	addLink(p1, "fact:a", "topic:t", "", "", "")
	addLink(p2, "fact:b", "topic:t", "", "", "")
	if got := NewLinkStore(dir, zap.NewNop()).Count(); got != 2 {
		t.Fatalf("concurrent adds lost: %d", got)
	}
	// A link added by another process is removable without a restart.
	if p1.Remove("fact:b", "topic:t", "") != 1 {
		t.Fatal("cannot remove a link another process added")
	}
}

func TestLinkStore_GCDanglingAfterGrace(t *testing.T) {
	dir := t.TempDir()
	ls := NewLinkStore(dir, zap.NewNop())
	addLink(ls, "fact:gone", "project:x", "", "", "")
	addLink(ls, "fact:here", "project:x", "", "", "")
	exists := func(id string) bool { return id != "fact:gone" }

	if n := ls.GC(exists); n != 0 || ls.Count() != 2 {
		t.Fatalf("first GC must only mark, dropped %d", n)
	}
	// Endpoint comes back within the grace period: the mark clears.
	if n := ls.GC(func(string) bool { return true }); n != 0 {
		t.Fatal("unexpected drop")
	}
	for _, l := range ls.All() {
		if !l.MissingSince.IsZero() {
			t.Fatal("missing mark not cleared")
		}
	}
	// Missing for longer than the grace period: dropped.
	ls.GC(exists)
	backdate(t, ls, func(l *StoredLink) {
		if l.From == "fact:gone" {
			l.MissingSince = time.Now().Add(-linkDanglingGrace - time.Hour)
		}
	})
	if n := ls.GC(exists); n != 1 || ls.Count() != 1 {
		t.Fatalf("dangling link not collected: dropped %d, live %d", n, ls.Count())
	}
	// Old tombstones are purged from the file.
	backdate(t, ls, func(l *StoredLink) {
		if l.Deleted {
			l.UpdatedAt = time.Now().Add(-linkTombstoneTTL - time.Hour)
		}
	})
	ls.GC(exists)
	ls.mu.RLock()
	total := len(ls.links)
	ls.mu.RUnlock()
	if total != 1 {
		t.Fatalf("expired tombstone kept: %d records", total)
	}
	if ls.GC(nil) != 0 {
		t.Fatal("nil exists must be a no-op")
	}
}

// backdate applies fn to every record in memory AND on disk — the state a
// store is really in after time passes (both copies age together).
func backdate(t *testing.T, ls *LinkStore, fn func(*StoredLink)) {
	t.Helper()
	ls.mu.Lock()
	defer ls.mu.Unlock()
	out := make([]StoredLink, 0, len(ls.links))
	for _, l := range ls.links {
		fn(l)
		out = append(out, *l)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(ls.path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLinkStore_CorruptFileQuarantined(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "links.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	ls := NewLinkStore(dir, zap.NewNop())
	if ls.Count() != 0 {
		t.Fatal("corrupt store loaded data")
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Fatalf("corrupt file not quarantined: %v", err)
	}
	if !addLink(ls, "fact:a", "topic:b", "", "", "") {
		t.Fatal("store unusable after quarantine")
	}
}

func TestLinkStore_SealedWithoutKeyIsReadOnly(t *testing.T) {
	resetLockedStoresForTest()
	dir := t.TempDir()
	t.Setenv(atrest.EnvKey, "key-A")
	addLink(NewLinkStore(dir, zap.NewNop()), "fact:a", "topic:b", "", "", "")
	path := filepath.Join(dir, "links.json")
	before, err := os.ReadFile(path)
	if err != nil || !atrest.IsEncrypted(before) {
		t.Fatalf("links.json must be sealed: %v", err)
	}
	t.Setenv(atrest.EnvKey, "key-B")
	locked := NewLinkStore(dir, zap.NewNop())
	addLink(locked, "fact:x", "topic:y", "", "", "")
	locked.Remove("fact:a", "topic:b", "")
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("sealed links.json overwritten without the key")
	}
	resetLockedStoresForTest()
}

func TestExportImport_CarriesLinks(t *testing.T) {
	src := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	addLink(src.Links, "fact:a", "project:x", "about", "user", "a note")
	var buf bytes.Buffer
	rep, err := src.Export(&buf)
	if err != nil || rep.Links != 1 {
		t.Fatalf("export: %+v %v", rep, err)
	}
	dst := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	irep, err := dst.Import(&buf)
	if err != nil || irep.Links != 1 {
		t.Fatalf("import: %+v %v", irep, err)
	}
	got := dst.Links.All()
	if len(got) != 1 || got[0].Rel != "about" || got[0].Note != "a note" {
		t.Fatalf("imported links = %+v", got)
	}
	// Importing the same file again changes nothing.
	buf.Reset()
	_, _ = src.Export(&buf)
	if again, _ := dst.Import(&buf); again.Links != 0 {
		t.Fatalf("re-import duplicated links: %+v", again)
	}
}

func TestManager_LinksMarkGraphDirty(t *testing.T) {
	m := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	m.graph.dirty.Store(false)
	addLink(m.Links, "fact:a", "topic:b", "", "", "")
	if !m.graph.dirty.Load() {
		t.Fatal("link mutation did not mark the graph dirty")
	}
}

func TestManager_GCLinksNeedsAGraph(t *testing.T) {
	m := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	addLink(m.Links, "fact:a", "topic:b", "", "", "")
	if n := m.GCLinks(); n != 0 || m.Links.Count() != 1 {
		t.Fatal("GC without a graph must never touch user links")
	}
}

// H2: LLM compaction rephrases facts (new content, new id); explicit links
// must follow the consolidated fact instead of dangling.
func TestRunCompaction_LinksFollowRephrasedFacts(t *testing.T) {
	m := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	m.Facts.AddFact("the ingest gateway terminates mTLS for collectors", "architecture", nil)
	var kept []string
	for _, line := range []string{
		"billing exports run nightly at two", "payroll uses a separate ledger",
		"dashboards refresh every minute", "backups rotate weekly to cold storage",
		"interns onboard through the wiki", "printers live on floor three",
		"vacation requests go through hr", "coffee machine descales on fridays",
		"badge readers log to syslog", "parking permits renew in january",
	} {
		m.Facts.AddFact(line, "general", nil)
		kept = append(kept, "- [general] "+line)
	}
	var gw string
	for _, f := range m.Facts.GetAll() {
		if strings.Contains(f.Content, "gateway") {
			gw = f.ID
		}
	}
	addLink(m.Links, "fact:"+gw, "project:orbital", "about", "user", "why")
	llm := func(context.Context, string) (string, error) {
		return "- [architecture] the ingest gateway terminates mTLS for edge collectors\n" + strings.Join(kept, "\n"), nil
	}
	if err := m.RunCompaction(context.Background(), llm); err != nil {
		t.Fatalf("compaction: %v", err)
	}
	var newGW string
	for _, f := range m.Facts.GetAll() {
		if strings.Contains(f.Content, "gateway") {
			newGW = f.ID
		}
	}
	if newGW == "" || newGW == gw {
		t.Fatalf("fixture: compaction must rewrite the fact (old %s, new %q)", gw, newGW)
	}
	links := m.Links.All()
	if len(links) != 1 || links[0].From != "fact:"+newGW || links[0].Note != "why" {
		t.Fatalf("link did not follow the rephrased fact: %+v", links)
	}
}

// N2: score-based compaction only archives; a link to an archived fact must
// never jump to a surviving fact that merely resembles it.
func TestCompactionRemap_OnlyToFactsTheConsolidationCreated(t *testing.T) {
	orig := []*Fact{
		{ID: "a", Content: "orbital uses postgres 15 for the main database"},
		{ID: "b", Content: "orbital uses postgres 15 for the analytics database"},
		{ID: "c", Content: "collectors retry with jitter"},
	}
	// "a" archived, "b" survives unchanged, "c" rephrased into "c2".
	consolidated := []*Fact{orig[1], {ID: "c2", Content: "collectors retry with exponential jitter"}}
	remap := compactionRemap(orig, consolidated)
	if _, moved := remap["fact:a"]; moved {
		t.Fatalf("archived fact remapped onto a survivor: %v", remap)
	}
	if remap["fact:c"] != "fact:c2" || len(remap) != 1 {
		t.Fatalf("remap = %v, want only c → c2", remap)
	}
	if compactionRemap(orig, orig) != nil {
		t.Fatal("nothing created: no remap")
	}
}

func TestRunCompaction_ConcurrentRunsAreSerialized(t *testing.T) {
	m := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	for _, f := range []string{"alpha one", "bravo two", "charlie three"} {
		m.Facts.AddFact(f, "general", nil)
	}
	llm := func(context.Context, string) (string, error) { return "- [general] alpha one", nil }
	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_ = m.RunCompaction(context.Background(), llm)
			done <- struct{}{}
		}()
	}
	<-done
	<-done
}
