/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestJournal_InPlaceMutationIsARewriteAndUndoRestoresIt(t *testing.T) {
	cli := journaledCLI(t)
	cli.history = longHistory(4)
	cli.syncTranscript()
	original := cli.history[3].Content
	// Microcompact/read-dedup shape: an earlier message shrinks while the
	// tail stays identical.
	cli.history[3].Content = "[stubbed]"
	cli.history = append(cli.history, models.Message{Role: "user", Content: "next"})
	cli.syncTranscript()
	events, err := cli.transcriptEvents()
	if err != nil {
		t.Fatal(err)
	}
	rewrites := 0
	for _, ev := range events {
		if ev.Kind == "rewrite" {
			rewrites++
		}
	}
	if rewrites != 1 {
		t.Fatalf("an in-place mutation must be journaled as a rewrite: %d", rewrites)
	}
	cli.preCompaction = nil
	if !cli.undoCompaction() || cli.history[3].Content != original {
		t.Fatalf("undo must restore the pre-mutation history: %q", cli.history[3].Content)
	}
}

func TestJournal_ToolCallsHashDistinctly(t *testing.T) {
	a := models.Message{Role: "assistant", ToolCalls: []models.ToolCall{{ID: "c1", Name: "read_file", Arguments: map[string]interface{}{"path": "a.go"}}}}
	b := models.Message{Role: "assistant", ToolCalls: []models.ToolCall{{ID: "c2", Name: "read_file", Arguments: map[string]interface{}{"path": "b.go"}}}}
	if messageHash(a) == messageHash(b) {
		t.Fatal("two calls with different ids/arguments must not collapse")
	}
	cli := journaledCLI(t)
	cli.history = []models.Message{{Role: "user", Content: "go"}, a, {Role: "tool", ToolCallID: "c1", Content: "x"}, b, {Role: "tool", ToolCallID: "c2", Content: "y"}}
	cli.syncTranscript()
	msgs, _ := cli.fullTranscript()
	if len(msgs) != 5 {
		t.Fatalf("every distinct tool call must be journaled: %d", len(msgs))
	}
}

func TestJournal_RepeatedTailMessagesAreRecorded(t *testing.T) {
	cli := journaledCLI(t)
	cli.history = []models.Message{{Role: "user", Content: "ok"}, {Role: "assistant", Content: "sure"}}
	cli.syncTranscript()
	cli.history = append(cli.history, models.Message{Role: "user", Content: "ok"}, models.Message{Role: "assistant", Content: "sure"})
	cli.syncTranscript()
	msgs, _ := cli.fullTranscript()
	if len(msgs) != 4 {
		t.Fatalf("identical follow-ups are new messages, not duplicates: %d", len(msgs))
	}
}

func TestJournal_ToleratesTornAndForeignLines(t *testing.T) {
	j, err := openTranscriptJournal(t.TempDir(), newTranscriptID(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Sync([]models.Message{{Role: "user", Content: "one"}}); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-append plus a stray line.
	f, _ := os.OpenFile(j.path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("not json at all\n{\"kind\":\"msg\",\"message\":{\"role\":\"user\",\"content\":\"torn")
	_ = f.Close()
	events, skipped, err := readJournalWithSkips(t, j.path)
	if err != nil || len(events) != 1 || skipped != 2 {
		t.Fatalf("torn and foreign lines must be skipped, not fatal: events=%d skipped=%d err=%v", len(events), skipped, err)
	}
	// The next append lands on a fresh line and stays readable; reopening
	// the journal (a resumed session) works too.
	if err := j.Sync([]models.Message{{Role: "user", Content: "one"}, {Role: "assistant", Content: "two"}}); err != nil {
		t.Fatal(err)
	}
	events, _, _ = readJournalWithSkips(t, j.path)
	if len(events) != 2 {
		t.Fatalf("append after a torn line must be readable: %d", len(events))
	}
	if _, err := openTranscriptJournal(t.TempDir(), "x"); err != nil {
		t.Fatal(err)
	}
	reopened, err := openTranscriptJournal(strings.TrimSuffix(j.path, "/"+j.id+".jsonl"), j.id)
	if err != nil || len(reopened.seen) != 2 {
		t.Fatalf("reopen must tolerate the damaged journal: %v seen=%d", err, len(reopened.seen))
	}
	// A very long line (beyond the old 64 MB scanner cap would be slow; 3 MB
	// proves there is no fixed cap in the reader).
	big := models.Message{Role: "user", Content: strings.Repeat("x", 3*1024*1024)}
	if err := j.Sync([]models.Message{{Role: "user", Content: "one"}, {Role: "assistant", Content: "two"}, big}); err != nil {
		t.Fatal(err)
	}
	if events, _, _ = readJournalWithSkips(t, j.path); len(events) != 3 {
		t.Fatalf("long lines must read back: %d", len(events))
	}
	_ = zap.NewNop()
}

func readJournalWithSkips(t *testing.T, path string) ([]transcriptEvent, int, error) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	return decodeTranscriptEvents(f, path)
}
