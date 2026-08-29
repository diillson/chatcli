/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package taskgraph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateLoadAndListRuns(t *testing.T) {
	base := t.TempDir()
	g1 := &Graph{Name: "first", Tasks: []*Task{{ID: "T1", Prompt: "p", Status: StatusDone}}, CreatedAt: time.Now().Add(-time.Hour)}
	if _, err := CreateRun(base, g1); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	g2 := &Graph{Name: "second", Tasks: []*Task{{ID: "T1", Prompt: "p", Status: StatusFailed}}}
	s2, err := CreateRun(base, g2)
	if err != nil {
		t.Fatalf("CreateRun 2: %v", err)
	}
	if g2.RunID == "" || g2.RunID != s2.RunID() {
		t.Fatalf("run id not stamped: %q vs %q", g2.RunID, s2.RunID())
	}

	loaded, err := s2.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if loaded.Name != "second" || loaded.Tasks[0].Status != StatusFailed {
		t.Fatalf("round trip: %+v", loaded)
	}

	rows, err := ListRuns(base)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(rows) != 2 || rows[0].Name != "second" {
		t.Fatalf("newest-first listing: %+v", rows)
	}
	if rows[0].Failed != 1 || rows[1].Done != 1 {
		t.Fatalf("counters: %+v", rows)
	}
}

func TestLoadStateQuarantinesCorrupt(t *testing.T) {
	base := t.TempDir()
	g := &Graph{Name: "x", Tasks: []*Task{{ID: "T1", Prompt: "p"}}}
	s, err := CreateRun(base, g)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	statePath := filepath.Join(s.Dir(), stateFileName)
	if err := os.WriteFile(statePath, []byte("{torn"), filePerm); err != nil {
		t.Fatalf("corrupt write: %v", err)
	}
	_, err = s.LoadState()
	if err == nil || !strings.Contains(err.Error(), "quarantined") {
		t.Fatalf("corrupt state must be quarantined: %v", err)
	}
	if _, statErr := os.Stat(statePath + ".corrupt"); statErr != nil {
		t.Fatalf("quarantine file missing: %v", statErr)
	}
	if _, statErr := os.Stat(statePath); !os.IsNotExist(statErr) {
		t.Fatal("corrupt file must not remain under the real name")
	}
}

func TestAppendEventAndOpenRunValidation(t *testing.T) {
	base := t.TempDir()
	g := &Graph{Name: "x", Tasks: []*Task{{ID: "T1", Prompt: "p"}}}
	s, err := CreateRun(base, g)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.AppendEvent(Event{Task: "T1", Type: EventTaskStarted}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := s.AppendEvent(Event{Type: EventRunFinished, Detail: "done"}); err != nil {
		t.Fatalf("AppendEvent 2: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir(), eventsFileName))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], EventTaskStarted) {
		t.Fatalf("ndjson lines: %q", lines)
	}

	if _, err := OpenRun(base, s.RunID()); err != nil {
		t.Fatalf("OpenRun: %v", err)
	}
	if _, err := OpenRun(base, "../escape"); err == nil {
		t.Fatal("path-traversal run id must be rejected")
	}
	if _, err := OpenRun(base, "tg-nope"); err == nil {
		t.Fatal("missing run must error")
	}
}

func TestPruneRuns(t *testing.T) {
	base := t.TempDir()
	mk := func(name string) *RunStore {
		g := &Graph{Name: name, Tasks: []*Task{{ID: "T1", Prompt: "p", Status: StatusDone}}}
		s, err := CreateRun(base, g)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		return s
	}
	old1, old2, fresh, active := mk("old1"), mk("old2"), mk("fresh"), mk("active")
	past := time.Now().Add(-48 * time.Hour)
	for _, s := range []*RunStore{old1, old2, active} {
		if err := os.Chtimes(filepath.Join(s.Dir(), stateFileName), past, past); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	removed, err := PruneRuns(base, 24*time.Hour, active.RunID())
	if err != nil {
		t.Fatalf("PruneRuns: %v", err)
	}
	if removed != 2 {
		t.Fatalf("want 2 removed, got %d", removed)
	}
	rows, _ := ListRuns(base)
	left := map[string]bool{}
	for _, r := range rows {
		left[r.RunID] = true
	}
	if !left[fresh.RunID()] || !left[active.RunID()] || len(rows) != 2 {
		t.Fatalf("survivors wrong: %+v", rows)
	}

	// olderThan<=0 removes everything except the skip id.
	removed, err = PruneRuns(base, 0, active.RunID())
	if err != nil || removed != 1 {
		t.Fatalf("prune all: %d %v", removed, err)
	}
	if rows, _ := ListRuns(base); len(rows) != 1 || rows[0].RunID != active.RunID() {
		t.Fatalf("active run must survive prune all: %+v", rows)
	}

	if n, err := PruneRuns(filepath.Join(base, "missing"), time.Hour, ""); err != nil || n != 0 {
		t.Fatalf("missing dir must be a no-op: %d %v", n, err)
	}
}
