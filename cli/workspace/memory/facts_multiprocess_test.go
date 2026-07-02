package memory

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// The REPL and the gateway daemon are separate processes sharing the same
// memory directory. Each FactIndex persists by rewriting the whole file from
// its in-memory map, so without reconciliation the slower writer erases every
// fact the other process learned — silent cross-process memory loss.

// TestFactIndexPersistMergesOtherProcessFacts: A and B open the same store;
// each learns a different fact; whoever persists last must not clobber the
// other's fact.
func TestFactIndexPersistMergesOtherProcessFacts(t *testing.T) {
	dir := t.TempDir()
	nop := zap.NewNop()

	a := NewFactIndex(dir, DefaultConfig(), nop)
	b := NewFactIndex(dir, DefaultConfig(), nop)

	if !a.AddFact("service alpha listens on port 3001 behind nginx", "general", nil) {
		t.Fatal("A's fact not added")
	}
	if !b.AddFact("service beta stores queue state in redis db 2", "general", nil) {
		t.Fatal("B's fact not added")
	}

	// A fresh reader sees the union, not just the last writer's view.
	c := NewFactIndex(dir, DefaultConfig(), nop)
	if got := c.Count(); got != 2 {
		t.Fatalf("cross-process clobber: fresh index has %d facts, want 2", got)
	}
}

// TestFactIndexForgetIsNotResurrectedByMerge: reconciliation must respect
// deletions. A forgets a fact; A's next persist (which merges with the file)
// must not adopt the forgotten fact back from disk.
func TestFactIndexForgetIsNotResurrectedByMerge(t *testing.T) {
	dir := t.TempDir()
	nop := zap.NewNop()

	a := NewFactIndex(dir, DefaultConfig(), nop)
	a.AddFact("service alpha listens on port 3001 behind nginx", "general", nil)
	a.AddFact("service beta stores queue state in redis db 2", "general", nil)

	if removed := a.ForgetMatching("redis db 2"); len(removed) != 1 {
		t.Fatalf("ForgetMatching removed %d, want 1", len(removed))
	}
	// Trigger another persist cycle (any mutation persists).
	a.AddFact("service gamma emits metrics to statsd on 8125", "general", nil)

	c := NewFactIndex(dir, DefaultConfig(), nop)
	if got := c.Count(); got != 2 {
		t.Fatalf("forgotten fact resurrected by merge: %d facts, want 2", got)
	}
	for _, f := range c.GetAll() {
		if f.Content == "service beta stores queue state in redis db 2" {
			t.Fatal("forgotten fact came back from disk")
		}
	}
}

// TestFactIndexCrossProcessForgetPropagates: A forgets a fact both processes
// know; when B persists afterwards, B must not write the fact back (the
// tombstone on disk outranks B's stale in-memory copy).
func TestFactIndexCrossProcessForgetPropagates(t *testing.T) {
	dir := t.TempDir()
	nop := zap.NewNop()

	a := NewFactIndex(dir, DefaultConfig(), nop)
	a.AddFact("service alpha listens on port 3001 behind nginx", "general", nil)

	b := NewFactIndex(dir, DefaultConfig(), nop) // B loads the fact too
	if b.Count() != 1 {
		t.Fatal("B did not load the shared fact")
	}

	if removed := a.ForgetMatching("port 3001"); len(removed) != 1 {
		t.Fatal("A's forget failed")
	}

	// B persists something new — its stale copy of the forgotten fact must
	// not survive the reconciliation.
	b.AddFact("service beta stores queue state in redis db 2", "general", nil)

	c := NewFactIndex(dir, DefaultConfig(), nop)
	for _, f := range c.GetAll() {
		if f.Content == "service alpha listens on port 3001 behind nginx" {
			t.Fatal("cross-process forget did not propagate: B resurrected the fact")
		}
	}
}

// TestMarkAccessedIsDebounced: access-metadata bumps happen on every
// retrieval; rewriting the whole index each time is wasted IO and widens the
// multi-process clobber window. Within the flush interval, MarkAccessed must
// not persist.
func TestMarkAccessedIsDebounced(t *testing.T) {
	dir := t.TempDir()
	nop := zap.NewNop()

	fi := NewFactIndex(dir, DefaultConfig(), nop)
	fi.AddFact("service alpha listens on port 3001 behind nginx", "general", nil)
	id := fi.hashContent("service alpha listens on port 3001 behind nginx")

	// First access flushes (interval elapsed since zero time).
	fi.MarkAccessed([]string{id})

	// Remove the file out from under the index: a debounced second access
	// must NOT rewrite it.
	path := filepath.Join(dir, "memory_index.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	fi.MarkAccessed([]string{id})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("MarkAccessed persisted within the flush interval — full-file rewrite per retrieval")
	}

	// A real mutation still persists immediately (and carries the pending
	// access metadata with it).
	fi.AddFact("service beta stores queue state in redis db 2", "general", nil)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("mutation did not persist: %v", err)
	}
}
