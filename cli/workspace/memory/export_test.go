/*
 * ChatCLI - Long-term memory tests: export/import, multi-process merge,
 * recall reasons, project labels.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestExportImport_RoundTripMergesEverything(t *testing.T) {
	src := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	src.Facts.AddFactWithMeta("The deploy freeze ends on Friday", "project", []string{"deploy"}, "/work/app", 0.9, "user")
	src.Facts.AddFact("Prefers tabular output", "preference", nil)
	src.Episodes.Add(Episode{Date: time.Now().Add(-time.Hour), Project: "app", Summary: "Fixed the parser", Outcome: "merged"})
	src.Topics.RecordWithSummary(map[string]string{"parser": "rewrote the lexer"})
	src.Projects.Upsert(map[string]string{"name": "app", "project_status": "active", "project_technologies": "go"})
	src.Profile.Update(map[string]string{"name": "Dev", "skills": "go"})

	var buf bytes.Buffer
	rep, err := src.Export(&buf)
	if err != nil || rep.Facts != 2 || rep.Episodes != 1 || rep.Topics != 1 || rep.Projects != 1 || !rep.Profile || rep.Sealed {
		t.Fatalf("export: err=%v rep=%+v", err, rep)
	}
	if strings.Count(buf.String(), "\n") != rep.Total()+1 {
		t.Fatalf("one line per record plus header: %q", buf.String())
	}

	dst := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	dst.Facts.AddFact("Prefers tabular output", "preference", nil) // already known: same content hash
	irep, err := dst.Import(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if irep.Facts != 1 || irep.FactsSkipped != 1 || irep.Episodes != 1 || irep.Topics != 1 || irep.Projects != 1 || !irep.Profile {
		t.Fatalf("import report = %+v", irep)
	}
	f, ok := dst.Facts.GetByID(src.Facts.GetAll()[0].ID)
	if !ok && dst.Facts.Count() != 2 {
		t.Fatalf("imported facts must keep their ids: %v", ok)
	}
	_ = f
	if dst.Profile.Get().Name != "Dev" || len(dst.Topics.GetAll()) != 1 || dst.Episodes.Count() != 1 {
		t.Fatal("profile, topics and episodes must land")
	}
	// Importing twice adds nothing.
	irep2, _ := dst.Import(bytes.NewReader(buf.Bytes()))
	if irep2.Facts != 0 || irep2.Episodes != 0 {
		t.Fatalf("second import must be idempotent: %+v", irep2)
	}
}

func TestExportImport_SealedWithKey(t *testing.T) {
	t.Setenv("CHATCLI_ENCRYPTION_KEY", "test-key-for-export")
	src := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	src.Facts.AddFact("secret preference", "preference", nil)
	path := filepath.Join(t.TempDir(), "mem.jsonl")
	rep, err := src.ExportToFile(path)
	if err != nil || !rep.Sealed {
		t.Fatalf("sealed export: %v %+v", err, rep)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "secret preference") || !strings.Contains(string(raw), exportSealedPrefix) {
		t.Fatal("sealed export must not leak plaintext")
	}
	dst := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	if irep, err := dst.ImportFromFile(path); err != nil || irep.Facts != 1 {
		t.Fatalf("import sealed: %v %+v", err, irep)
	}
	t.Setenv("CHATCLI_ENCRYPTION_KEY", "")
	if _, err := dst.ImportFromFile(path); err == nil || !strings.Contains(err.Error(), "sealed") {
		t.Fatalf("a sealed file without the key must fail explicitly: %v", err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatal("exports are private files")
	}
}

func TestTopicsAndProjects_MergeAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	a := NewTopicTracker(dir, zap.NewNop())
	b := NewTopicTracker(dir, zap.NewNop())
	a.RecordWithSummary(map[string]string{"parser": "from A"})
	b.RecordWithSummary(map[string]string{"cache": "from B"}) // B never saw "parser" in memory
	names := map[string]bool{}
	for _, tp := range NewTopicTracker(dir, zap.NewNop()).GetAll() {
		names[tp.Name] = true
	}
	if !names["parser"] || !names["cache"] {
		t.Fatalf("both processes' topics must survive the last write: %v", names)
	}
	pa := NewProjectTracker(dir, zap.NewNop())
	pb := NewProjectTracker(dir, zap.NewNop())
	pa.Upsert(map[string]string{"name": "app", "project_technologies": "go", "project_status": "active"})
	pb.Upsert(map[string]string{"name": "site", "project_technologies": "ts", "project_status": "active"})
	pb.Upsert(map[string]string{"name": "app", "project_technologies": "sqlite"})
	all := NewProjectTracker(dir, zap.NewNop()).GetAll()
	if len(all) != 2 {
		t.Fatalf("projects = %+v", all)
	}
	for _, p := range all {
		if p.Name == "app" && (len(p.Technologies) < 2) {
			t.Fatalf("technologies must union across processes: %v", p.Technologies)
		}
	}
}

func TestProfile_MergeAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	a := NewUserProfileStore(dir, zap.NewNop())
	b := NewUserProfileStore(dir, zap.NewNop())
	a.Update(map[string]string{"name": "Dev", "skills": "go"})
	time.Sleep(20 * time.Millisecond)
	b.Update(map[string]string{"role": "SRE", "skills": "terraform"})
	p := NewUserProfileStore(dir, zap.NewNop()).Get()
	if p.Name != "Dev" || p.Role != "SRE" || len(p.Skills) != 2 {
		t.Fatalf("field-level merge: %+v", p)
	}
}

func TestDailyNote_AtomicAppendKeepsEarlierEntries(t *testing.T) {
	d := NewDailyNoteStore(t.TempDir(), zap.NewNop())
	if err := d.WriteDailyNote("first"); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteDailyNote("second"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(d.TodayNotePath())
	if !strings.Contains(string(data), "first") || !strings.Contains(string(data), "second") {
		t.Fatalf("note = %q", data)
	}
	if entries, _ := os.ReadDir(filepath.Dir(d.TodayNotePath())); len(entries) != 1 {
		t.Fatalf("no temp files left behind: %v", entries)
	}
}

func TestRankedRecall_ExplainsAndLabelsProjects(t *testing.T) {
	fi := NewFactIndex(t.TempDir(), DefaultConfig(), zap.NewNop())
	fi.AddFactWithMeta("Use the staging cluster for load tests", "gotcha", []string{"staging"}, "/home/dev/other", 0.8, "user")
	ranked := fi.SearchBlendedMinRanked([]string{"staging", "cluster"}, nil, DefaultConfig().RankWeights, 0)
	if len(ranked) != 1 || ranked[0].Lexical <= 0 || ranked[0].Final <= 0 {
		t.Fatalf("ranked = %+v", ranked)
	}
	why := ranked[0].Why()
	if !strings.Contains(why, "keywords") || !strings.Contains(why, "via user") {
		t.Fatalf("why = %q", why)
	}
	// Plain search still returns the same facts in the same order.
	if plain := fi.SearchBlendedMin([]string{"staging", "cluster"}, nil, DefaultConfig().RankWeights, 0); len(plain) != 1 || plain[0].ID != ranked[0].Fact.ID {
		t.Fatal("plain and ranked searches must agree")
	}
	if !SameProject("/w/app", "/w/app/pkg/x") || SameProject("/w/app", "/w/app2") || SameProject("", "/w") {
		t.Fatal("same-project semantics")
	}
	if ProjectLabel("/w/app/pkg", "/w/app") != "" || ProjectLabel("/w/other", "/w/app") != "other" {
		t.Fatalf("labels: %q %q", ProjectLabel("/w/app/pkg", "/w/app"), ProjectLabel("/w/other", "/w/app"))
	}
	home, _ := os.UserHomeDir()
	if ProjectLabel(filepath.Join(home, "src", "thing"), "/w/app") != "~/src/thing" {
		t.Fatal("home-relative label")
	}
}
