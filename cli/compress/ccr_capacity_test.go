/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDiskStorePutOversizedEntryNeverDangles guards the reversibility contract
// at the store level: a Put must never "succeed" and then have its entry
// evicted by the very same call's size-cap enforcement. Before the per-entry
// cap existed, storing content larger than maxBytes returned a valid key whose
// file was already gone — any marker embedded for that key dangled forever.
func TestDiskStorePutOversizedEntryNeverDangles(t *testing.T) {
	store, err := NewDiskStore(t.TempDir(), 1000, 0)
	if err != nil {
		t.Fatalf("NewDiskStore: %v", err)
	}

	content := strings.Repeat("x", 2000) // twice the store cap

	key, err := store.Put(content)
	if err != nil {
		// Desired behavior: the store refuses the oversized entry outright so
		// the caller (offload) degrades to passthrough and never emits a marker.
		if !errors.Is(err, ErrEntryTooLarge) {
			t.Fatalf("Put rejected oversized entry with unexpected error: %v", err)
		}
		return
	}

	// If Put claims success, the content MUST be retrievable — otherwise the
	// marker embedded for this key dangles.
	got, ok, gerr := store.Get(key)
	if gerr != nil {
		t.Fatalf("Get(%q): %v", key, gerr)
	}
	if !ok {
		t.Fatalf("dangling marker: Put returned key %q but Get reports the entry gone (evicted by the same call)", key)
	}
	if got != content {
		t.Fatalf("Get(%q) returned different content", key)
	}
}

// TestDiskStorePerEntryCapPreservesExistingEntries verifies the cap protects
// the rest of the store too: an oversized Put must not wipe smaller, valid
// entries on its way in.
func TestDiskStorePerEntryCapPreservesExistingEntries(t *testing.T) {
	store, err := NewDiskStore(t.TempDir(), 1000, 0)
	if err != nil {
		t.Fatalf("NewDiskStore: %v", err)
	}

	small := strings.Repeat("a", 100)
	smallKey, err := store.Put(small)
	if err != nil {
		t.Fatalf("Put(small): %v", err)
	}

	if _, err := store.Put(strings.Repeat("x", 5000)); !errors.Is(err, ErrEntryTooLarge) {
		t.Fatalf("Put(oversized) = %v; want ErrEntryTooLarge", err)
	}

	if _, ok, _ := store.Get(smallKey); !ok {
		t.Fatalf("oversized Put evicted a valid small entry (key %q)", smallKey)
	}
}

// TestLayerNeverEmitsUnrecallableMarker is the end-to-end contract check: for
// any payload the layer compresses, every CCR marker present in the output
// MUST be recallable. With a store cap smaller than the payload, the old
// behavior emitted a marker whose original had already been evicted — the
// model was promised "@recall" and got a miss.
func TestLayerNeverEmitsUnrecallableMarker(t *testing.T) {
	store, err := NewDiskStore(t.TempDir(), 1024, 0) // 1KB cap, payload below is ~4x larger
	if err != nil {
		t.Fatalf("NewDiskStore: %v", err)
	}
	layer := NewLayer(Config{Mode: ModeLossyWithCCR, Threshold: 100, Store: store})

	// A realistic grep-style payload large enough to trigger lossy reduction
	// and far larger than the store cap.
	var b strings.Builder
	for i := 0; i < 60; i++ {
		b.WriteString("internal/service/handler.go:")
		b.WriteString(strings.Repeat("4", 2))
		b.WriteString(": func handleRequest(w http.ResponseWriter, r *http.Request) error { // occurrence\n")
	}
	payload := b.String()

	out, res := layer.CompressToolOutput("@search", payload)

	for _, key := range ExtractKeys(out) {
		if _, ok := layer.Recall(key); !ok {
			t.Fatalf("layer emitted unrecallable marker <<ccr:%s>> (strategy=%s): reversibility contract broken", key, res.Strategy)
		}
	}
}

// TestBoundedMemoryStoreLRUAndPerEntryCap verifies the fallback store honors
// the same contracts as DiskStore: LRU eviction to the cap, per-entry
// capacity refusal, and idempotent puts.
func TestBoundedMemoryStoreLRUAndPerEntryCap(t *testing.T) {
	s := NewBoundedMemoryStore(80) // per-entry capacity: 20

	if _, err := s.Put(strings.Repeat("x", 21)); !errors.Is(err, ErrEntryTooLarge) {
		t.Fatalf("Put(oversized) = %v; want ErrEntryTooLarge", err)
	}

	kOld, err := s.Put(strings.Repeat("a", 20))
	if err != nil {
		t.Fatalf("Put(oldest): %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	var kNew string
	for _, c := range []string{"b", "c", "d", "e"} {
		if kNew, err = s.Put(strings.Repeat(c, 20)); err != nil {
			t.Fatalf("Put(%s): %v", c, err)
		}
	}

	if _, ok, _ := s.Get(kOld); ok {
		t.Fatal("bounded MemoryStore did not evict the oldest entry")
	}
	if _, ok, _ := s.Get(kNew); !ok {
		t.Fatal("bounded MemoryStore evicted the newest entry")
	}
	if st := s.Stats(); st.TotalBytes > st.MaxBytes {
		t.Fatalf("footprint %d exceeds cap %d", st.TotalBytes, st.MaxBytes)
	}
}

// TestUnboundedMemoryStoreStillUnbounded pins the historical behavior of the
// test/one-shot constructor: no cap, no per-entry refusal.
func TestUnboundedMemoryStoreStillUnbounded(t *testing.T) {
	s := NewMemoryStore()
	key, err := s.Put(strings.Repeat("x", 1<<20))
	if err != nil {
		t.Fatalf("unbounded Put: %v", err)
	}
	if _, ok, _ := s.Get(key); !ok {
		t.Fatal("unbounded MemoryStore lost an entry")
	}
}

// TestLayerFromEnvSurfacesStoreFallback verifies that when the persistent CCR
// store cannot be opened the layer (a) still works on a bounded in-memory
// store and (b) exposes the cause via StoreFallback instead of degrading
// silently.
func TestLayerFromEnvSurfacesStoreFallback(t *testing.T) {
	// Point the CCR dir below a regular FILE so MkdirAll must fail.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHATCLI_COMPRESSION", "lossy-with-ccr")
	t.Setenv("CHATCLI_COMPRESSION_CCR_DIR", filepath.Join(blocker, "ccr"))

	layer := NewLayerFromEnv("")
	if layer.StoreFallback() == nil {
		t.Fatal("StoreFallback() = nil; want the disk-store open error surfaced")
	}

	// The fallback store must be live (bounded memory): recall works in-process.
	key, err := layer.store.Put("fallback payload")
	if err != nil {
		t.Fatalf("fallback store Put: %v", err)
	}
	if got, ok := layer.Recall(key); !ok || got != "fallback payload" {
		t.Fatalf("fallback store Recall = (%q, %v)", got, ok)
	}
	if st := layer.store.Stats(); st.MaxBytes <= 0 {
		t.Fatal("fallback MemoryStore is unbounded; want the configured size cap applied")
	}
}

// TestLayerFromEnvNoFallbackOnHealthyDisk pins the happy path: a writable
// stateDir yields a disk store and StoreFallback() == nil.
func TestLayerFromEnvNoFallbackOnHealthyDisk(t *testing.T) {
	t.Setenv("CHATCLI_COMPRESSION", "lossy-with-ccr")
	t.Setenv("CHATCLI_COMPRESSION_CCR_DIR", "")

	layer := NewLayerFromEnv(t.TempDir())
	if err := layer.StoreFallback(); err != nil {
		t.Fatalf("StoreFallback() = %v; want nil on healthy disk", err)
	}
}
