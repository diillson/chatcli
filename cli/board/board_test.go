/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package board

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "board.json"))
}

func TestCreateAndGet(t *testing.T) {
	s := newTestStore(t)
	c, err := s.Create("Implement feature X", "details here", "coder", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "card-1" || c.Column != ColBacklog || c.Assignee != "coder" {
		t.Fatalf("unexpected card: %+v", c)
	}
	got, err := s.Get(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Implement feature X" || got.Description != "details here" {
		t.Fatalf("Get mismatch: %+v", got)
	}
}

func TestCreateRejectsEmptyTitle(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("  ", "", "", ""); err == nil {
		t.Fatal("empty title must be rejected")
	}
}

func TestMoveRecordsHistory(t *testing.T) {
	s := newTestStore(t)
	c, _ := s.Create("T", "", "", "")
	moved, err := s.Move(c.ID, ColDoing, "orchestrator")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Column != ColDoing || len(moved.History) != 1 {
		t.Fatalf("move not recorded: %+v", moved)
	}
	h := moved.History[0]
	if h.From != ColBacklog || h.To != ColDoing || h.By != "orchestrator" {
		t.Fatalf("bad transition: %+v", h)
	}
	// Same-column move is a no-op, not a new history entry.
	again, err := s.Move(c.ID, ColDoing, "orchestrator")
	if err != nil {
		t.Fatal(err)
	}
	if len(again.History) != 1 {
		t.Fatalf("no-op move added history: %+v", again.History)
	}
}

func TestMoveUnknownCard(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Move("card-99", ColDone, "x"); err == nil {
		t.Fatal("expected error for unknown card")
	}
}

func TestAssignNoteAndLinks(t *testing.T) {
	s := newTestStore(t)
	c, _ := s.Create("T", "", "", "")

	if _, err := s.Assign(c.ID, "reviewer"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddNote(c.ID, "reviewer", "LGTM with nits"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddNote(c.ID, "reviewer", "  "); err == nil {
		t.Fatal("empty note must be rejected")
	}
	if _, err := s.LinkRun(c.ID, "run-7"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkRun(c.ID, "run-7"); err != nil { // dedup
		t.Fatal(err)
	}
	updated, err := s.LinkJob(c.ID, "job-3")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Assignee != "reviewer" || len(updated.Notes) != 1 ||
		len(updated.RunIDs) != 1 || len(updated.JobIDs) != 1 {
		t.Fatalf("unexpected card state: %+v", updated)
	}
}

func TestListOrderAndFilter(t *testing.T) {
	s := newTestStore(t)
	a, _ := s.Create("A", "", "", "")
	b, _ := s.Create("B", "", "", "")
	cCard, _ := s.Create("C", "", "", "")
	_, _ = s.Move(b.ID, ColDone, "x")
	_, _ = s.Move(cCard.ID, ColDoing, "x")

	all, err := s.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 cards, got %d", len(all))
	}
	// Column display order: backlog (A), doing (C), done (B).
	if all[0].ID != a.ID || all[1].ID != cCard.ID || all[2].ID != b.ID {
		t.Fatalf("wrong order: %s %s %s", all[0].ID, all[1].ID, all[2].ID)
	}

	doing, _ := s.List(ColDoing)
	if len(doing) != 1 || doing[0].ID != cCard.ID {
		t.Fatalf("filter failed: %+v", doing)
	}
}

func TestPersistenceAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.json")
	s1 := NewStore(path)
	c, err := s1.Create("Persisted", "", "coder", "")
	if err != nil {
		t.Fatal(err)
	}
	s2 := NewStore(path)
	got, err := s2.Get(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Persisted" {
		t.Fatalf("persistence broken: %+v", got)
	}
	// Seq continues — no ID reuse after reopen.
	c2, _ := s2.Create("Second", "", "", "")
	if c2.ID != "card-2" {
		t.Fatalf("seq not persisted: %s", c2.ID)
	}
}

func TestCorruptFileIsSurfacedNotWiped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	if _, err := s.List(""); err == nil {
		t.Fatal("corrupt file must surface an error")
	}
	if _, err := s.Create("X", "", "", ""); err == nil {
		t.Fatal("mutation over corrupt file must fail, not wipe it")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "{not json" {
		t.Fatal("corrupt file was overwritten")
	}
}

func TestArchive(t *testing.T) {
	s := newTestStore(t)
	a, _ := s.Create("A", "", "", "")
	b, _ := s.Create("B", "", "", "")
	_, _ = s.Move(a.ID, ColDone, "x")

	n, err := s.Archive(0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 archived, got %d", n)
	}
	if _, err := s.Get(a.ID); err == nil {
		t.Fatal("archived card still present")
	}
	if _, err := s.Get(b.ID); err != nil {
		t.Fatal("non-done card was archived")
	}

	// Age-gated archive keeps fresh done cards.
	_, _ = s.Move(b.ID, ColDone, "x")
	n, err = s.Archive(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("fresh done card archived: %d", n)
	}
}

func TestParseColumnAliases(t *testing.T) {
	cases := map[string]Column{
		"backlog": ColBacklog, "todo": ColBacklog,
		"doing": ColDoing, "in_progress": ColDoing, "wip": ColDoing,
		"review": ColReview, "qa": ColReview,
		"blocked": ColBlocked, "waiting": ColBlocked,
		"done": ColDone, "delivered": ColDone, "DONE": ColDone,
	}
	for in, want := range cases {
		got, err := ParseColumn(in)
		if err != nil || got != want {
			t.Fatalf("ParseColumn(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseColumn("nope"); err == nil {
		t.Fatal("invalid column must error")
	}
}

func TestConcurrentMutations(t *testing.T) {
	s := newTestStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := s.Create("t", "", "", "")
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := s.Move(c.ID, ColDoing, "g"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	all, err := s.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 16 {
		t.Fatalf("expected 16 cards, got %d", len(all))
	}
	seen := map[string]bool{}
	for _, c := range all {
		if seen[c.ID] {
			t.Fatalf("duplicate card ID %s", c.ID)
		}
		seen[c.ID] = true
	}
}
