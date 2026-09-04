/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestWorkerWindow_JournalsUnderTheParentWithoutPollutingIt(t *testing.T) {
	t.Setenv("CHATCLI_ENCRYPTION_KEY", "")
	path := filepath.Join(t.TempDir(), "tr-parent.jsonl")
	j := &transcriptJournal{id: "tr-parent", path: path, seen: map[string]bool{}}
	parent := []models.Message{{Role: "user", Content: "orchestrate"}, {Role: "assistant", Content: "dispatching"}}
	if err := j.Sync(parent); err != nil {
		t.Fatal(err)
	}
	c := &ChatCLI{logger: zap.NewNop(), transcript: j, historyCompactor: NewHistoryCompactor(zap.NewNop())}
	w := &workerWindow{cli: c}
	w.NoteTurn("researcher", []models.Message{{Role: "assistant", Content: "worker step"}, {Role: "tool", Content: "tool out"}})
	w.NoteTurn("", []models.Message{{Role: "assistant", Content: "unnamed is dropped"}})
	msgs, err := readTranscript(path)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("parent history must not carry worker turns: %d %v", len(msgs), err)
	}
	workerEvents, _ := readWorkerTranscript(path)
	if len(workerEvents) != 2 || workerEvents[0].Worker != "researcher" {
		t.Fatalf("worker turns must be journaled under the parent: %+v", workerEvents)
	}
	// Reopening the journal rebuilds the parent's state from parent
	// messages only.
	j2 := &transcriptJournal{id: "tr-parent", path: path, seen: map[string]bool{}}
	for _, m := range msgs { // what openTranscriptJournal seeds on reopen
		j2.seen[messageHash(m)] = true
	}
	if err := j2.Sync(append(parent, models.Message{Role: "user", Content: "next"})); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := readTranscript(path); len(msgs) != 3 {
		t.Fatalf("parent history after resync = %d", len(msgs))
	}
	if w.NeedsCompaction(nil) {
		t.Fatal("empty history never needs compaction")
	}
	if (&workerWindow{}).NeedsCompaction(parent) || func() bool { _, err := (&workerWindow{}).Compact(t.Context(), parent); return err != nil }() {
		t.Fatal("nil-safe")
	}
}
