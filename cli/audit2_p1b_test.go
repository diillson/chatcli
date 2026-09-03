/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func journaledCLI(t *testing.T) *ChatCLI {
	t.Helper()
	t.Setenv("CHATCLI_SESSION_TRANSCRIPT", "true")
	j, err := openTranscriptJournal(t.TempDir(), newTranscriptID(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	return &ChatCLI{logger: zap.NewNop(), costTracker: NewCostTracker(), transcript: j}
}

func longHistory(n int) []models.Message {
	h := []models.Message{{Role: "system", Content: "charter"}}
	for i := 0; i < n; i++ {
		h = append(h, models.Message{Role: "user", Content: "question number " + strings.Repeat("x", i) + " about the parser refactor"},
			models.Message{Role: "assistant", Content: "answer number " + strings.Repeat("y", i) + " covering the lexer state machine"})
	}
	return h
}

func TestJournalRewriteCarriesHashesAndResolves(t *testing.T) {
	cli := journaledCLI(t)
	cli.history = longHistory(4)
	cli.syncTranscript()
	full := cli.history
	// Rewrite: summary replaces the middle.
	cli.history = append([]models.Message{full[0], {Role: "user", Content: "[STRUCTURED SUMMARY] the parser work", Meta: &models.MessageMeta{IsSummary: true, SummaryOf: 6}}}, full[7:]...)
	cli.syncTranscript()
	events, err := cli.transcriptEvents()
	if err != nil {
		t.Fatal(err)
	}
	var rewrite *transcriptEvent
	for i := range events {
		if events[i].Kind == "rewrite" {
			rewrite = &events[i]
		}
	}
	if rewrite == nil || len(rewrite.Hashes) != len(full) {
		t.Fatalf("rewrite event must carry the replaced history's hashes: %+v", rewrite)
	}
	restored, ok := resolveHashes(transcriptIndex(events), rewrite.Hashes)
	if !ok || len(restored) != len(full) || restored[3].Content != full[3].Content {
		t.Fatalf("hashes must resolve to the pre-rewrite history: ok=%v len=%d", ok, len(restored))
	}
	if _, ok := resolveHashes(transcriptIndex(events), []string{"missing"}); ok {
		t.Fatal("a missing hash must fail the resolution")
	}
}

func TestUndoCompaction_FromMemoryThenFromJournal(t *testing.T) {
	cli := journaledCLI(t)
	cli.history = longHistory(4)
	cli.syncTranscript()
	full := len(cli.history)
	cli.beforeCompaction(t.Context(), compactTriggerAuto)
	cli.history = append([]models.Message{cli.history[0], {Role: "user", Content: "[STRUCTURED SUMMARY] parser", Meta: &models.MessageMeta{IsSummary: true}}}, cli.history[7:]...)
	cli.syncTranscript()
	if !cli.undoCompaction() || len(cli.history) != full {
		t.Fatalf("in-memory undo must restore %d messages, got %d", full, len(cli.history))
	}
	// The undo itself is a checkpoint (the compacted view can come back).
	if len(cli.checkpoints) == 0 || cli.checkpoints[len(cli.checkpoints)-1].MsgCount != 4 {
		t.Fatalf("the compacted view must be checkpointed: %+v", cli.checkpoints)
	}
	// Second undo: nothing in memory, but the journal's rewrite event.
	cli.preCompaction = nil
	cli.history = cli.history[:4]
	cli.syncTranscript() // another rewrite (shrink) recorded with hashes of the full history
	cli.preCompaction = nil
	if !cli.undoCompaction() || len(cli.history) != full {
		t.Fatalf("journal-backed undo must restore %d messages, got %d", full, len(cli.history))
	}
	// Nothing left anywhere: a fresh CLI without journal.
	bare := &ChatCLI{logger: zap.NewNop(), costTracker: NewCostTracker(), history: longHistory(1)}
	if bare.undoCompaction() {
		t.Fatal("no snapshot and no journal must be a no-op")
	}
}

func TestCheckpointsPersistThroughTheJournal(t *testing.T) {
	cli := journaledCLI(t)
	cli.history = longHistory(3)
	cli.syncTranscript()
	cli.saveCheckpoint()
	cli.history = append(cli.history, models.Message{Role: "user", Content: "one more"})
	cli.syncTranscript()
	cli.saveCheckpoint()
	recs := cli.checkpointRecords()
	if len(recs) != 2 || len(recs[0].Hashes) != 7 || len(recs[1].Hashes) != 8 {
		t.Fatalf("records = %+v", recs)
	}
	sd := cli.buildSessionData()
	if len(sd.Checkpoints) != 2 {
		t.Fatal("session data must carry the checkpoints")
	}
	// A resumed CLI sharing the journal rebuilds them; one with a pruned
	// journal drops the unresolvable ones.
	restored := cli.restoreCheckpoints(recs)
	if len(restored) != 2 || restored[0].MsgCount != 7 || restored[1].History[7].Content != "one more" {
		t.Fatalf("restored = %+v", restored)
	}
	recs[1].Hashes[0] = "gone"
	if got := cli.restoreCheckpoints(recs); len(got) != 1 {
		t.Fatalf("unresolvable checkpoint must be dropped, got %d", len(got))
	}
	if (&ChatCLI{}).restoreCheckpoints(recs) != nil {
		t.Fatal("no journal → no checkpoints")
	}
}

func TestSessionExport_MarkdownAndJSONL(t *testing.T) {
	cli := journaledCLI(t)
	cli.history = longHistory(2)
	cli.history = append(cli.history, models.Message{Role: "assistant", ToolCalls: []models.ToolCall{{ID: "c1", Name: "read_file", Arguments: map[string]interface{}{"path": "a.go"}}}},
		models.Message{Role: "tool", ToolCallID: "c1", Content: "package main"})
	cli.syncTranscript()
	// The compacted live view must not shrink the export: the journal is the source.
	cli.history = cli.history[:2]
	cli.syncTranscript()
	dir := t.TempDir()
	md := filepath.Join(dir, "out.md")
	cli.handleSessionExport("md", md)
	body, err := os.ReadFile(md)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# ChatCLI transcript", "<details><summary>#1 system</summary>", "### #2 user", "// tool call read_file", "tool result `c1`", "package main"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("markdown export missing %q:\n%s", want, body)
		}
	}
	jl := filepath.Join(dir, "out.jsonl")
	cli.handleSessionExport("jsonl", jl)
	lines, err := os.ReadFile(jl)
	if err != nil || strings.Count(strings.TrimSpace(string(lines)), "\n")+1 < 2 {
		t.Fatalf("jsonl export: err=%v body=%q", err, lines)
	}
	if fi, _ := os.Stat(md); fi.Mode().Perm() != 0o600 {
		t.Fatalf("exports are private files: %v", fi.Mode())
	}
	cli.handleSessionExport("xml", filepath.Join(dir, "no")) // usage, no file
	if _, err := os.Stat(filepath.Join(dir, "no")); err == nil {
		t.Fatal("unknown format must not write")
	}
}

func TestTranscriptSearchAndSnippet(t *testing.T) {
	cli := journaledCLI(t)
	cli.history = longHistory(3)
	cli.history = append(cli.history, models.Message{Role: "assistant", Content: "The deploy freeze ends on Friday after the release train."})
	cli.syncTranscript()
	msgs, source := cli.fullTranscript()
	if source != "journal" || len(msgs) != 8 {
		t.Fatalf("source=%s len=%d", source, len(msgs))
	}
	snip := transcriptSnippet("aaa bbb ccc deploy freeze ends on friday and then some more words here to pad", "freeze friday", 30)
	if !strings.Contains(snip, "freeze") || !strings.HasPrefix(snip, "…") {
		t.Fatalf("snippet = %q", snip)
	}
	if truncateRunes("héllo", 2) != "h" {
		t.Fatal("rune-safe cut")
	}
	cli.searchTranscript("deploy freeze") // prints; must not panic with system docs blank
	cli.showTranscript(7, 5)
	cli.handleSessionTranscript([]string{"search", "release", "train"}, "search release train")
	cli.handleRewindCommand("/rewind bogus")
	cli.handleRewindCommand("/rewind compact")
}

func TestSanitizeFileStem(t *testing.T) {
	if sanitizeFileStem("my session/2026") != "my-session-2026" || sanitizeFileStem("///") != "transcript" {
		t.Fatal(sanitizeFileStem("my session/2026"))
	}
}
