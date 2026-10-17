/*
 * ChatCLI - Long-term memory tests: sealed-store read-only latch.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/atrest"
	"go.uber.org/zap"
)

func readAll(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		out[e.Name()] = b
	}
	return out
}

func TestSealedStores_LockReadOnlyWithoutTheKey(t *testing.T) {
	resetLockedStoresForTest()
	dir := t.TempDir()
	t.Setenv(atrest.EnvKey, "key-A")
	m := NewManager(dir, DefaultConfig(), zap.NewNop())
	m.Facts.AddFact("the deploy freeze ends on friday", "project", nil)
	m.Episodes.Add(Episode{Summary: "fixed the parser", Project: "app"})
	m.Topics.RecordWithSummary(map[string]string{"parser": "rewrote the lexer"})
	m.Projects.Upsert(map[string]string{"name": "app", "project_status": "active"})
	m.Profile.Update(map[string]string{"name": "Dev"})
	m.Patterns.RecordSessionStart()
	before := readAll(t, dir)
	if len(before) < 5 {
		t.Fatalf("expected sealed stores on disk, got %v", before)
	}
	for name, b := range before {
		if strings.HasSuffix(name, ".json") && !atrest.IsEncrypted(b) {
			t.Fatalf("%s must be sealed", name)
		}
	}

	for _, key := range []string{"", "key-B"} {
		resetLockedStoresForTest()
		t.Setenv(atrest.EnvKey, key)
		m2 := NewManager(dir, DefaultConfig(), zap.NewNop())
		if m2.Facts.Count() != 0 {
			t.Fatal("unreadable store loads empty in memory")
		}
		// Every write path is a no-op while locked.
		m2.Facts.AddFact("a new fact that must not be written", "general", nil)
		m2.Episodes.Add(Episode{Summary: "new episode", Project: "x"})
		m2.Topics.RecordWithSummary(map[string]string{"cache": "new"})
		m2.Projects.Upsert(map[string]string{"name": "site", "project_status": "active"})
		m2.Profile.Update(map[string]string{"role": "SRE"})
		m2.Patterns.RecordSessionStart()
		after := readAll(t, dir)
		for name, b := range before {
			if !bytes.Equal(b, after[name]) {
				t.Fatalf("key %q: sealed %s was overwritten", key, name)
			}
		}
		locked := LockedStores()
		if len(locked) < 5 {
			t.Fatalf("key %q: locked stores must be reported, got %v", key, locked)
		}
	}

	// With the retired key listed, everything opens and writes resume.
	resetLockedStoresForTest()
	t.Setenv(atrest.EnvKey, "key-B")
	t.Setenv(atrest.EnvPreviousKeys, "key-A")
	m3 := NewManager(dir, DefaultConfig(), zap.NewNop())
	if m3.Facts.Count() != 1 || len(LockedStores()) != 0 {
		t.Fatalf("retired key must unlock: facts=%d locked=%v", m3.Facts.Count(), LockedStores())
	}
	m3.Facts.AddFact("second fact", "general", nil)
	if m3.Facts.Count() != 2 {
		t.Fatal("writes must resume once the store opens")
	}
	// A fresh install (no files) is writable — the latch is about sealed
	// files only.
	resetLockedStoresForTest()
	t.Setenv(atrest.EnvKey, "")
	t.Setenv(atrest.EnvPreviousKeys, "")
	fresh := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	fresh.Facts.AddFact("first fact ever", "general", nil)
	if fresh.Facts.Count() != 1 || len(LockedStores()) != 0 {
		t.Fatal("fresh install must be writable")
	}
}

// Daily notes hold what happened in each turn, so they are sealed like the
// other stores. Appending reopens the sealed day (a second append must not
// corrupt it), and a note edited by hand in plaintext still reads.
func TestDailyNotes_SealedAppendableAndHandEditsRead(t *testing.T) {
	t.Setenv(atrest.EnvKey, "daily-key")
	d := NewDailyNoteStore(t.TempDir(), zap.NewNop())
	if err := d.WriteDailyNote("first"); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteDailyNote("second"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(d.TodayNotePath())
	if !atrest.IsEncrypted(raw) || strings.Contains(string(raw), "first") {
		t.Fatalf("daily notes must be sealed on disk: %q", raw)
	}
	notes := d.GetRecentDailyNotes(1)
	if len(notes) != 1 || !strings.Contains(notes[0].Content, "first") || !strings.Contains(notes[0].Content, "second") {
		t.Fatal("readers must see both entries")
	}

	if err := os.WriteFile(d.TodayNotePath(), []byte("## edited by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteDailyNote("third"); err != nil {
		t.Fatal(err)
	}
	notes = d.GetRecentDailyNotes(1)
	if len(notes) != 1 || !strings.Contains(notes[0].Content, "edited by hand") || !strings.Contains(notes[0].Content, "third") {
		t.Fatalf("a hand-edited note must keep reading and appending: %+v", notes)
	}
}

func TestMultiProcessMerge_WorksOnSealedStores(t *testing.T) {
	resetLockedStoresForTest()
	t.Setenv(atrest.EnvKey, "merge-key")
	dir := t.TempDir()
	a := NewTopicTracker(dir, zap.NewNop())
	b := NewTopicTracker(dir, zap.NewNop())
	a.RecordWithSummary(map[string]string{"parser": "from A"})
	b.RecordWithSummary(map[string]string{"cache": "from B"})
	names := map[string]bool{}
	for _, tp := range NewTopicTracker(dir, zap.NewNop()).GetAll() {
		names[tp.Name] = true
	}
	if !names["parser"] || !names["cache"] {
		t.Fatalf("sealed stores must still merge across processes: %v", names)
	}
	pa := NewUserProfileStore(dir, zap.NewNop())
	pb := NewUserProfileStore(dir, zap.NewNop())
	pa.Update(map[string]string{"name": "Dev"})
	pb.Update(map[string]string{"role": "SRE"})
	if p := NewUserProfileStore(dir, zap.NewNop()).Get(); p.Name != "Dev" || p.Role != "SRE" {
		t.Fatalf("sealed profile merge: %+v", p)
	}
}

// A rollup never digests a week or month whose notes are sealed with a key
// this process lacks: the digest would be written once without them and
// never rebuilt.
func TestRollup_DeferredWhileASourceNoteIsSealed(t *testing.T) {
	dir := t.TempDir()
	monday := startOfISOWeek(time.Now().AddDate(0, 0, -70))
	writeDailyNoteAt(t, dir, monday, "plain day")
	t.Setenv(atrest.EnvKey, "rollup-key-rollup-key-rollup-key-00")
	sealedDay := monday.AddDate(0, 0, 1)
	path := filepath.Join(dir, sealedDay.Format("200601"), sealedDay.Format("20060102")+".md")
	if err := atomicWriteFile(path, []byte("## 10:00\n\n- sealed day\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(atrest.EnvKey, "")

	rs := NewRollupStore(dir, zap.NewNop())
	if _, err := rs.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	year, week := monday.ISOWeek()
	if _, err := os.Stat(filepath.Join(dir, "weekly", fmt.Sprintf("%04d-W%02d.md", year, week))); !os.IsNotExist(err) {
		t.Fatal("a weekly digest was built without the sealed note")
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "monthly")); len(entries) != 0 {
		t.Fatal("a monthly digest was built without the sealed note")
	}

	t.Setenv(atrest.EnvKey, "rollup-key-rollup-key-rollup-key-00")
	if _, err := rs.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "weekly", fmt.Sprintf("%04d-W%02d.md", year, week))); err != nil {
		t.Fatal("the digest was not built once the key was back")
	}
}
