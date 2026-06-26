/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestKeyForDeterministicAndShort(t *testing.T) {
	k1 := KeyFor("hello world")
	k2 := KeyFor("hello world")
	if k1 != k2 {
		t.Fatalf("KeyFor not deterministic: %q != %q", k1, k2)
	}
	if len(k1) != keyLen {
		t.Fatalf("key length = %d, want %d", len(k1), keyLen)
	}
	if KeyFor("different") == k1 {
		t.Fatal("distinct content produced identical keys")
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	key := KeyFor("payload")
	marker := FormatMarker(key)
	body := "head text " + marker + " tail " + FormatMarker(key) + " " + FormatMarker(KeyFor("other"))
	got := ExtractKeys(body)
	if len(got) != 2 {
		t.Fatalf("ExtractKeys returned %d keys, want 2 (deduped): %v", len(got), got)
	}
	if got[0] != key {
		t.Fatalf("first key = %q, want %q", got[0], key)
	}
	if ExtractKeys("no markers here") != nil {
		t.Fatal("expected nil for marker-free content")
	}
}

func TestMemoryStoreRoundTrip(t *testing.T) {
	s := NewMemoryStore()
	key, err := s.Put("the original payload")
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(key)
	if err != nil || !ok {
		t.Fatalf("Get miss: ok=%v err=%v", ok, err)
	}
	if got != "the original payload" {
		t.Fatalf("Get returned %q", got)
	}
	if _, ok, _ := s.Get("deadbeefdeadbeef"); ok {
		t.Fatal("Get of unknown key reported a hit")
	}
}

func TestDiskStoreRoundTripAndByteIdentity(t *testing.T) {
	dir := t.TempDir()
	s, err := NewDiskStore(dir, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	original := strings.Repeat("line of log output\n", 500)
	key, err := s.Put(original)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(key)
	if err != nil || !ok {
		t.Fatalf("Get miss: ok=%v err=%v", ok, err)
	}
	if got != original {
		t.Fatal("DiskStore did not return a byte-identical original")
	}
}

func TestDiskStoreDedupes(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewDiskStore(dir, 0, 0)
	k1, _ := s.Put("same content")
	k2, _ := s.Put("same content")
	if k1 != k2 {
		t.Fatalf("dedup failed: %q != %q", k1, k2)
	}
	if st := s.Stats(); st.Entries != 1 {
		t.Fatalf("expected 1 entry after dedup, got %d", st.Entries)
	}
}

func TestDiskStorePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s1, _ := NewDiskStore(dir, 0, 0)
	key, _ := s1.Put("persisted across restart")

	s2, err := NewDiskStore(dir, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, _ := s2.Get(key)
	if !ok || got != "persisted across restart" {
		t.Fatalf("reopened store lost data: ok=%v got=%q", ok, got)
	}
}

func TestDiskStoreLRUEviction(t *testing.T) {
	dir := t.TempDir()
	// Cap at 30 bytes; each payload is 20 bytes, so only one fits at a time.
	s, _ := NewDiskStore(dir, 30, 0)
	kOld, _ := s.Put(strings.Repeat("a", 20))
	// Make the second Put strictly newer.
	time.Sleep(10 * time.Millisecond)
	kNew, _ := s.Put(strings.Repeat("b", 20))

	if _, ok, _ := s.Get(kOld); ok {
		t.Fatal("LRU eviction did not drop the oldest entry")
	}
	if _, ok, _ := s.Get(kNew); !ok {
		t.Fatal("LRU eviction dropped the newest entry")
	}
	if st := s.Stats(); st.TotalBytes > st.MaxBytes {
		t.Fatalf("footprint %d exceeds cap %d after eviction", st.TotalBytes, st.MaxBytes)
	}
}

func TestDiskStoreTTLPruneOnReopen(t *testing.T) {
	dir := t.TempDir()
	s1, _ := NewDiskStore(dir, 0, 0)
	key, _ := s1.Put("stale entry")
	// Backdate the file well beyond the TTL.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(s1.path(key), old, old); err != nil {
		t.Fatal(err)
	}

	s2, _ := NewDiskStore(dir, 0, time.Hour)
	if _, ok, _ := s2.Get(key); ok {
		t.Fatal("TTL prune did not drop the stale entry on reopen")
	}
}

func TestDiskStorePruneRemovesStaleAndReports(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewDiskStore(dir, 0, time.Hour)
	kStale, _ := s.Put("stale payload")
	kFresh, _ := s.Put("fresh payload")

	// Backdate the stale entry beyond the TTL (in-memory recency drives prune).
	s.mu.Lock()
	s.entries[kStale].lastAccess = time.Now().Add(-2 * time.Hour)
	s.mu.Unlock()

	res := s.Prune()
	if res.Removed != 1 {
		t.Fatalf("Removed = %d, want 1", res.Removed)
	}
	if res.BytesFreed != int64(len("stale payload")) {
		t.Fatalf("BytesFreed = %d, want %d", res.BytesFreed, len("stale payload"))
	}
	if res.RemainingEntries != 1 {
		t.Fatalf("RemainingEntries = %d, want 1", res.RemainingEntries)
	}
	if _, ok, _ := s.Get(kStale); ok {
		t.Fatal("stale entry survived Prune")
	}
	if _, ok, _ := s.Get(kFresh); !ok {
		t.Fatal("Prune dropped the fresh entry")
	}
}

func TestDiskStoreOnPutTTLSweep(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewDiskStore(dir, 0, time.Hour)
	kOld, _ := s.Put("old payload")

	// Backdate the entry and force the sweep throttle open so the next Put
	// triggers a mid-session TTL sweep (the daemon long-run gap).
	s.mu.Lock()
	s.entries[kOld].lastAccess = time.Now().Add(-2 * time.Hour)
	s.lastTTLSweep = time.Now().Add(-2 * ccrTTLSweepInterval)
	s.mu.Unlock()

	if _, err := s.Put("new payload"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(kOld); ok {
		t.Fatal("on-Put TTL sweep did not curate the stale entry")
	}
}

func TestDiskStoreStatsReportsStaleAndOldest(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewDiskStore(dir, 0, time.Hour)
	kOld, _ := s.Put("old payload")
	_, _ = s.Put("fresh payload")

	s.mu.Lock()
	s.entries[kOld].lastAccess = time.Now().Add(-3 * time.Hour)
	s.mu.Unlock()

	st := s.Stats()
	if st.StaleEntries != 1 {
		t.Fatalf("StaleEntries = %d, want 1", st.StaleEntries)
	}
	if st.TTL != time.Hour {
		t.Fatalf("TTL = %v, want 1h", st.TTL)
	}
	if st.OldestAge < 3*time.Hour {
		t.Fatalf("OldestAge = %v, want >= 3h", st.OldestAge)
	}
}

func TestLayerPruneDelegatesToPruner(t *testing.T) {
	dir := t.TempDir()
	ds, err := NewDiskStore(dir, 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	l := NewLayer(Config{Mode: ModeLossyWithCCR, Store: ds})

	kStale, _ := ds.Put("stale payload")
	_, _ = ds.Put("fresh payload")
	ds.mu.Lock()
	ds.entries[kStale].lastAccess = time.Now().Add(-2 * time.Hour)
	ds.mu.Unlock()

	res := l.Prune()
	if res.Removed != 1 {
		t.Fatalf("Layer.Prune Removed = %d, want 1", res.Removed)
	}
	if res.RemainingEntries != 1 {
		t.Fatalf("RemainingEntries = %d, want 1", res.RemainingEntries)
	}
}

func TestLayerPruneNilSafe(t *testing.T) {
	var l *Layer
	if got := l.Prune(); got != (PruneResult{}) {
		t.Fatalf("nil Layer.Prune = %+v, want zero", got)
	}
	l2 := NewLayer(Config{Mode: ModeLossyWithCCR, Store: nil})
	if got := l2.Prune(); got != (PruneResult{}) {
		t.Fatalf("nil-store Layer.Prune = %+v, want zero", got)
	}
}

func TestMemoryStorePruneIsNoop(t *testing.T) {
	m := NewMemoryStore()
	_, _ = m.Put("a")
	_, _ = m.Put("bb")
	res := m.Prune()
	if res.Removed != 0 {
		t.Fatalf("MemoryStore.Prune removed %d, want 0 (unbounded)", res.Removed)
	}
	if res.RemainingEntries != 2 {
		t.Fatalf("RemainingEntries = %d, want 2", res.RemainingEntries)
	}
}

func TestDiskStoreGetHandlesVanishedFile(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewDiskStore(dir, 0, 0)
	key, _ := s.Put("will be deleted underneath")
	if err := os.Remove(s.path(key)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(key)
	if err != nil {
		t.Fatalf("Get of vanished file returned error: %v", err)
	}
	if ok || got != "" {
		t.Fatal("Get should report a clean miss when the file vanished")
	}
}
