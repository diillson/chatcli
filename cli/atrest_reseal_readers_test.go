/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/atrest"
	"go.uber.org/zap"
)

// resealFixture roots HOME and the state root in a temp dir, so the stores
// resolve their default paths under it and the reseal walk covers them.
func resealFixture(t *testing.T) (*ChatCLI, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // windows
	t.Setenv(atrest.EnvKey, "")
	t.Setenv(atrest.EnvPreviousKeys, "")
	root := filepath.Join(home, ".chatcli")
	return &ChatCLI{logger: zap.NewNop(), stateRoot: root}, root
}

func mustReseal(t *testing.T, c *ChatCLI, wantRewritten int) {
	t.Helper()
	t.Setenv(atrest.EnvKey, "reseal-readers-key")
	rep := c.resealAtRestStores(0)
	if len(rep.Errors) != 0 || rep.Rewritten != wantRewritten {
		t.Fatalf("reseal report = %+v, want %d rewritten and no errors", rep, wantRewritten)
	}
}

func TestReseal_CostSnapshotStaysReadable(t *testing.T) {
	c, root := resealFixture(t)
	dir := filepath.Join(root, "costs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(SessionCostData{SessionID: "sess-1", TotalRequests: 3})
	if err := os.WriteFile(filepath.Join(dir, "sess-1.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	mustReseal(t, c, 1)

	got, err := LoadCostSnapshot("sess-1")
	if err != nil {
		t.Fatalf("cost snapshot must open after reseal: %v", err)
	}
	if got.TotalRequests != 3 {
		t.Fatalf("TotalRequests = %d, want 3", got.TotalRequests)
	}

	// A snapshot written by the tracker with the key on also round-trips.
	ct := NewCostTracker()
	ct.storeDir = dir
	ct.sessionID = "sess-2"
	ct.RecordUsage("OPENAI", "gpt-test", 10, 5)
	if err := ct.SaveSession(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCostSnapshot("sess-2"); err != nil {
		t.Fatalf("sealed snapshot must open: %v", err)
	}
}

func TestReseal_CCREntryStaysReadable(t *testing.T) {
	c, root := resealFixture(t)
	dir := filepath.Join(root, "ccr")
	s, err := compress.NewDiskStore(dir, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.Put("offloaded tool output body")
	if err != nil {
		t.Fatal(err)
	}
	mustReseal(t, c, 1)

	// The live store and a fresh one (the adopt-from-disk path) both read it.
	if got, ok, err := s.Get(key); err != nil || !ok || got != "offloaded tool output body" {
		t.Fatalf("live store Get after reseal = %q %v %v", got, ok, err)
	}
	fresh, err := compress.NewDiskStore(dir, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, err := fresh.Get(key); err != nil || !ok || got != "offloaded tool output body" {
		t.Fatalf("reopened store Get after reseal = %q %v %v", got, ok, err)
	}
}

func TestReseal_MemoryPendingSegmentStaysReadable(t *testing.T) {
	c, root := resealFixture(t)
	mw := &memoryWorker{cli: c, logger: zap.NewNop(), pendingDir: filepath.Join(root, "memory", "pending")}
	path, err := mw.persistPending([]models.Message{{Role: "user", Content: "remember the staging cluster name"}})
	if err != nil {
		t.Fatal(err)
	}
	mustReseal(t, c, 1)

	seg, err := readPendingSegment(path)
	if err != nil {
		t.Fatalf("pending segment must open after reseal: %v", err)
	}
	if len(seg.Messages) != 1 || mw.pendingBacklogCount() != 1 {
		t.Fatalf("segment = %+v, backlog = %d", seg, mw.pendingBacklogCount())
	}

	// A segment queued with the key on is sealed and still counted.
	if _, err := mw.persistPending([]models.Message{{Role: "user", Content: "second"}}); err != nil {
		t.Fatal(err)
	}
	if n := mw.pendingBacklogCount(); n != 2 {
		t.Fatalf("backlog with a sealed segment = %d, want 2", n)
	}
}
