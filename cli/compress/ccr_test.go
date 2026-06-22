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
