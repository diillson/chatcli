/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"os"
	"testing"
)

// The interactive REPL and the gateway daemon are separate processes sharing
// the same CCR directory (~/.chatcli/ccr). The directory is the source of
// truth (content-addressed filenames, no index file); each process's in-memory
// index is only a cache of it. These tests pin the two behaviors that make
// that sharing correct: reads adopt entries written by the other process, and
// curation reconciles accounting with what is actually on disk.

// TestDiskStoreCrossProcessRecall: a key offloaded by process A (writer) must
// be recallable by process B (reader) whose in-memory index predates the
// write. Without disk-fallback adoption in Get, B misses forever — @recall in
// the gateway daemon fails for markers created in the REPL and vice versa.
func TestDiskStoreCrossProcessRecall(t *testing.T) {
	dir := t.TempDir()

	reader, err := NewDiskStore(dir, 0, 0) // opened BEFORE the write exists
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewDiskStore(dir, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	key, err := writer.Put("offloaded by the other process")
	if err != nil {
		t.Fatal(err)
	}

	got, ok, err := reader.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("cross-process recall miss: entry exists on disk but the reader's stale index hides it")
	}
	if got != "offloaded by the other process" {
		t.Fatalf("Get returned %q", got)
	}
	// Adoption must also fix the reader's accounting.
	if st := reader.Stats(); st.Entries == 0 || st.TotalBytes == 0 {
		t.Fatalf("reader did not adopt the entry into its index: %+v", st)
	}
}

// TestDiskStorePruneReconcilesExternalRemovals: entries evicted/deleted by
// another process must vanish from this process's accounting on the next
// curation pass, not linger as phantom footprint that skews eviction and
// Stats.
func TestDiskStorePruneReconcilesExternalRemovals(t *testing.T) {
	dir := t.TempDir()
	s, err := NewDiskStore(dir, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	k1, _ := s.Put("entry one — will be removed externally")
	k2, _ := s.Put("entry two — stays")

	// Simulate the other process evicting k1.
	if err := os.Remove(s.path(k1)); err != nil {
		t.Fatal(err)
	}

	res := s.Prune()
	if got := s.Stats(); got.Entries != 1 {
		t.Fatalf("Stats().Entries = %d after external removal + Prune; want 1 (prune result: %+v)", got.Entries, res)
	}
	if _, ok, _ := s.Get(k2); !ok {
		t.Fatal("reconciliation dropped a live entry")
	}
}

// TestDiskStorePruneAdoptsExternalAdditions: entries written by another
// process become visible in this process's Stats after a curation pass, so
// the size-cap eviction operates on the real footprint.
func TestDiskStorePruneAdoptsExternalAdditions(t *testing.T) {
	dir := t.TempDir()
	s, err := NewDiskStore(dir, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	other, err := NewDiskStore(dir, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Put("written by the other process"); err != nil {
		t.Fatal(err)
	}

	s.Prune()
	if got := s.Stats(); got.Entries != 1 {
		t.Fatalf("Stats().Entries = %d after external addition + Prune; want 1", got.Entries)
	}
}
