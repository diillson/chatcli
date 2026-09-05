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

	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/atrest"
	"go.uber.org/zap"
)

func TestTranscriptJournal_AppendsOnceAndRecordsRewrites(t *testing.T) {
	t.Setenv(atrest.EnvKey, "")
	dir := t.TempDir()
	j, err := openTranscriptJournal(dir, "tr-test")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	h := []models.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
	}
	if err := j.Sync(h); err != nil {
		t.Fatalf("sync: %v", err)
	}
	// Same history again: nothing new appended.
	if err := j.Sync(h); err != nil {
		t.Fatalf("sync: %v", err)
	}
	h = append(h, models.Message{Role: "tool", Content: "bytes", ToolCallID: "t1"})
	if err := j.Sync(h); err != nil {
		t.Fatalf("sync: %v", err)
	}
	msgs, err := readTranscript(j.path)
	if err != nil || len(msgs) != 4 {
		t.Fatalf("journal = %d msgs err=%v", len(msgs), err)
	}

	// Compaction rewrote the middle: a rewrite event plus only the NEW
	// summary message are appended; kept messages are not duplicated.
	compacted := []models.Message{
		h[0],
		{Role: "user", Content: "[STRUCTURED SUMMARY]", Meta: &models.MessageMeta{IsSummary: true}},
		h[3],
	}
	if err := j.Sync(compacted); err != nil {
		t.Fatalf("sync after rewrite: %v", err)
	}
	msgs, _ = readTranscript(j.path)
	if len(msgs) != 5 || msgs[4].Content != "[STRUCTURED SUMMARY]" {
		t.Fatalf("after rewrite journal = %d msgs, last=%q", len(msgs), msgs[len(msgs)-1].Content)
	}
	raw, _ := os.ReadFile(j.path)
	if !strings.Contains(string(raw), `"kind":"rewrite"`) {
		t.Fatal("rewrite event missing from the journal")
	}
	// The original u1/a1 are still in the record even though the window lost them.
	if msgs[1].Content != "u1" || msgs[2].Content != "a1" {
		t.Fatal("pre-compaction messages must stay in the journal")
	}
}

func TestTranscriptJournal_ResumeDoesNotDuplicate(t *testing.T) {
	t.Setenv(atrest.EnvKey, "")
	dir := t.TempDir()
	j, _ := openTranscriptJournal(dir, "tr-resume")
	h := []models.Message{{Role: "user", Content: "u1"}, {Role: "assistant", Content: "a1"}}
	_ = j.Sync(h)

	// A new process adopts the same id and syncs the same history.
	j2, err := openTranscriptJournal(dir, "tr-resume")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	h = append(h, models.Message{Role: "user", Content: "u2"})
	if err := j2.Sync(h); err != nil {
		t.Fatalf("sync: %v", err)
	}
	msgs, _ := readTranscript(j2.path)
	if len(msgs) != 3 {
		t.Fatalf("resume must not duplicate journaled messages, got %d", len(msgs))
	}
}

func TestTranscriptJournal_SealedLinesWhenEncryptionEnabled(t *testing.T) {
	t.Setenv(atrest.EnvKey, "journal-key")
	dir := t.TempDir()
	j, _ := openTranscriptJournal(dir, "tr-sealed")
	if err := j.Sync([]models.Message{{Role: "user", Content: "launch codes alpha"}}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	raw, _ := os.ReadFile(j.path)
	if strings.Contains(string(raw), "launch codes") || !strings.HasPrefix(string(raw), sealedLinePrefix) {
		t.Fatalf("journal line must be sealed: %q", raw)
	}
	msgs, err := readTranscript(j.path)
	if err != nil || len(msgs) != 1 || msgs[0].Content != "launch codes alpha" {
		t.Fatalf("sealed journal must read back: %v %+v", err, msgs)
	}
	t.Setenv(atrest.EnvKey, "")
	if _, err := readTranscript(j.path); err == nil {
		t.Fatal("sealed journal must not open without the key")
	}
}

func TestTranscriptJournal_InvalidIDAndPrune(t *testing.T) {
	dir := t.TempDir()
	if _, err := openTranscriptJournal(dir, "../escape"); err == nil {
		t.Fatal("path-traversal id must be rejected")
	}
	old := filepath.Join(dir, "tr-old.jsonl")
	_ = os.WriteFile(old, []byte("{}\n"), 0o600)
	stale := time.Now().Add(-100 * 24 * time.Hour)
	_ = os.Chtimes(old, stale, stale)
	fresh := filepath.Join(dir, "tr-new.jsonl")
	_ = os.WriteFile(fresh, []byte("{}\n"), 0o600)
	if n := pruneTranscripts(dir, 90*24*time.Hour); n != 1 {
		t.Fatalf("prune removed %d, want 1", n)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh journal must survive")
	}
	if n := pruneTranscripts(dir, 0); n != 0 {
		t.Fatal("zero ttl must keep everything")
	}
	t.Setenv(TranscriptEnv, "false")
	if transcriptEnabled() {
		t.Fatal("env kill switch not honored")
	}
	t.Setenv(TranscriptEnv, "")
	if !transcriptEnabled() {
		t.Fatal("default must be enabled")
	}
}

func TestMemoryFlush_NilSafe(t *testing.T) {
	cli := &ChatCLI{}
	cli.flushMemoryBeforeCompaction(nil) //nolint:staticcheck // nil ctx is the point: no worker, no work
	cli.queueMemoryBeforeCompaction()
	if seg := cli.unextractedSegment(); seg != nil {
		t.Fatalf("no worker → no segment, got %d", len(seg))
	}
}

func TestInitTranscriptJournal_LifecycleUnderTempHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(atrest.EnvKey, "")
	t.Setenv(TranscriptEnv, "")
	t.Setenv("CHATCLI_SESSION_TTL", "")

	cli := &ChatCLI{logger: zapNop()}
	cli.initTranscriptJournal("")
	if cli.transcriptID() == "" || !strings.HasPrefix(cli.transcriptID(), "tr-") {
		t.Fatalf("journal not opened at boot: %q", cli.transcriptID())
	}
	cli.history = []models.Message{{Role: "user", Content: "u1"}, {Role: "assistant", Content: "a1"}}
	cli.syncTranscript()
	dir, _ := transcriptDir()
	msgs, err := readTranscript(filepath.Join(dir, cli.transcriptID()+".jsonl"))
	if err != nil || len(msgs) != 2 {
		t.Fatalf("synced journal = %d msgs err=%v", len(msgs), err)
	}

	// A loaded session that carries its own id switches the journal.
	first := cli.transcriptID()
	cli.adoptTranscript("tr-20260903-000000-cafe0000")
	if cli.transcriptID() == first {
		t.Fatal("adoptTranscript must switch to the session's journal")
	}
	cli.adoptTranscript("") // empty id keeps the current journal
	if cli.transcriptID() != "tr-20260903-000000-cafe0000" {
		t.Fatal("empty id must keep the current journal")
	}
	cli.adoptTranscript("../escape")
	if cli.transcriptID() != "tr-20260903-000000-cafe0000" {
		t.Fatal("invalid id must not replace the journal")
	}

	// Session data round-trips the id; restore adopts it.
	sd := cli.buildSessionData()
	if sd.TranscriptID != cli.transcriptID() {
		t.Fatalf("session data id = %q", sd.TranscriptID)
	}
	other := &ChatCLI{logger: zapNop()}
	other.restoreSessionData(sd)
	if other.transcriptID() != sd.TranscriptID {
		t.Fatalf("restore must adopt the session journal, got %q", other.transcriptID())
	}

	// Disabled by env: no journal, sync is a no-op.
	t.Setenv(TranscriptEnv, "false")
	off := &ChatCLI{logger: zapNop()}
	off.initTranscriptJournal("")
	if off.transcriptID() != "" {
		t.Fatal("journal must stay closed when disabled")
	}
	off.syncTranscript()
}

func TestTranscriptTTL(t *testing.T) {
	t.Setenv("CHATCLI_SESSION_TTL", "")
	if got := transcriptTTL(); got != 90*24*time.Hour {
		t.Fatalf("default ttl = %s", got)
	}
	t.Setenv("CHATCLI_SESSION_TTL", "7d")
	if got := transcriptTTL(); got != 7*24*time.Hour {
		t.Fatalf("7d ttl = %s", got)
	}
	t.Setenv("CHATCLI_SESSION_TTL", "0")
	if got := transcriptTTL(); got != 0 {
		t.Fatalf("0 must disable expiry, got %s", got)
	}
	t.Setenv("CHATCLI_SESSION_TTL", "garbage")
	if got := transcriptTTL(); got != 90*24*time.Hour {
		t.Fatalf("garbage must keep the default, got %s", got)
	}
}

func TestMemoryFlush_QueuesUnextractedSegment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := workspace.NewMemoryStore(t.TempDir(), zapNop())
	cli := &ChatCLI{logger: zapNop(), memoryStore: store}
	cli.memWorker = newMemoryWorker(cli)
	cli.history = []models.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "a2"},
	}
	cli.memWorker.lastProcessedIdx = 3 // u1/a1 already extracted
	seg := cli.unextractedSegment()
	if len(seg) != 2 || seg[0].Content != "u2" {
		t.Fatalf("segment = %+v", seg)
	}
	cli.queueMemoryBeforeCompaction()
	if cli.memWorker.lastProcessedIdx != len(cli.history) {
		t.Fatalf("watermark not advanced: %d", cli.memWorker.lastProcessedIdx)
	}
	if files := cli.memWorker.pendingFiles(); len(files) != 1 {
		t.Fatalf("segment must be queued on the WAL, got %d files", len(files))
	}
	// Nothing left → nothing queued again.
	cli.queueMemoryBeforeCompaction()
	if files := cli.memWorker.pendingFiles(); len(files) != 1 {
		t.Fatalf("empty segment must not enqueue, got %d files", len(files))
	}
	// A lone trailing user turn is not worth distilling.
	cli.history = append(cli.history, models.Message{Role: "user", Content: "u3"})
	if seg := cli.unextractedSegment(); seg != nil {
		t.Fatalf("single user turn must not form a segment: %+v", seg)
	}
}

func zapNop() *zap.Logger { return zap.NewNop() }

// thinkingBlocks builds one signed reasoning block for the tests below.
func thinkingBlocks(sig string) []models.ThinkingBlock {
	return []models.ThinkingBlock{{Type: "thinking", Thinking: "let me check " + sig, Signature: sig}}
}

// TestMessageHash_ReasoningIsPartOfIdentity pins both halves of the
// contract: reasoning discriminates, and its absence changes nothing.
func TestMessageHash_ReasoningIsPartOfIdentity(t *testing.T) {
	base := models.Message{Role: "assistant", Content: "reading the file"}
	base.ToolCalls = []models.ToolCall{{ID: "call_1", Name: "read_file"}}

	a, b := base, base
	a.Thinking = thinkingBlocks("sig-A")
	b.Thinking = thinkingBlocks("sig-B")
	if messageHash(a) == messageHash(b) {
		t.Fatal("turns identical in content and calls but differing in reasoning must not share a hash")
	}
	if messageHash(a) == messageHash(base) {
		t.Fatal("a reasoning block must change the hash of the turn carrying it")
	}

	// Order is identity: the provider replays the sequence verbatim.
	two := base
	two.Thinking = []models.ThinkingBlock{a.Thinking[0], b.Thinking[0]}
	swapped := base
	swapped.Thinking = []models.ThinkingBlock{b.Thinking[0], a.Thinking[0]}
	if messageHash(two) == messageHash(swapped) {
		t.Fatal("reasoning order must be part of the hash")
	}

	// Migration guard: a message without reasoning hashes exactly as it did
	// before reasoning was persisted, so journals from earlier builds still
	// match their history. The constant is the pre-change digest.
	const preReasoningDigest = "1d83ebbe8302299c84843bdf174f521a3fa221d0cb5df16e507751f469be131b"
	if got := messageHash(models.Message{Role: "assistant", Content: "a1"}); got != preReasoningDigest {
		t.Fatalf("hash of a reasoning-free message changed: %s (journals written by earlier builds no longer match)", got)
	}
}

// TestTranscriptJournal_RewriteKeepsTwinTurnsWithDistinctReasoning covers
// the path where the collision actually cost a message: after a compaction
// the journal re-walks the history and skips what it has already seen, so
// a turn whose twin-in-text came earlier used to be dropped from the record.
func TestTranscriptJournal_RewriteKeepsTwinTurnsWithDistinctReasoning(t *testing.T) {
	t.Setenv(atrest.EnvKey, "")
	dir := t.TempDir()
	j, err := openTranscriptJournal(dir, "tr-twins")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// A retried tool loop: the same answer and the same call, twice, with
	// the reasoning that led there differing.
	twin := func(sig string) models.Message {
		return models.Message{
			Role:      "assistant",
			Content:   "running it",
			ToolCalls: []models.ToolCall{{ID: "call_1", Name: "run_command"}},
			Thinking:  thinkingBlocks(sig),
		}
	}
	history := []models.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "build it"},
		twin("sig-A"),
		{Role: "tool", Content: "exit 1", ToolCallID: "call_1"},
		twin("sig-B"),
		{Role: "tool", Content: "exit 0", ToolCallID: "call_1"},
	}
	if err := j.Sync(history); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Compaction rewrites the window: the tail survives, the head collapses.
	compacted := []models.Message{
		history[0],
		{Role: "user", Content: "[STRUCTURED SUMMARY]", Meta: &models.MessageMeta{IsSummary: true}},
		history[4],
		history[5],
	}
	if err := j.Sync(compacted); err != nil {
		t.Fatalf("sync after rewrite: %v", err)
	}

	msgs, err := readTranscript(j.path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var sigs []string
	for _, m := range msgs {
		for _, b := range m.Thinking {
			sigs = append(sigs, b.Signature)
		}
	}
	if len(sigs) != 2 || sigs[0] != "sig-A" || sigs[1] != "sig-B" {
		t.Fatalf("both turns must survive the rewrite with their own reasoning, got %v", sigs)
	}
}

// TestTranscriptJournal_ExtendsJournalWrittenBeforeReasoning proves the
// migration on a real file: a journal recorded by an earlier build is
// extended, not re-walked as a rewrite, and nothing is duplicated.
func TestTranscriptJournal_ExtendsJournalWrittenBeforeReasoning(t *testing.T) {
	t.Setenv(atrest.EnvKey, "")
	dir := t.TempDir()
	// Two events exactly as an earlier build wrote them: no thinking field.
	legacy := `{"ts":"2026-09-01T10:00:00Z","kind":"msg","message":{"role":"user","content":"u1"}}
{"ts":"2026-09-01T10:00:01Z","kind":"msg","message":{"role":"assistant","content":"a1"}}
`
	path := filepath.Join(dir, "tr-legacy.jsonl")
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	j, err := openTranscriptJournal(dir, "tr-legacy")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	history := []models.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"},
	}
	if err := j.Sync(history); err != nil {
		t.Fatalf("sync: %v", err)
	}
	msgs, _ := readTranscript(path)
	if len(msgs) != 3 || msgs[2].Content != "u2" {
		t.Fatalf("legacy journal = %d msgs, want the two legacy plus u2", len(msgs))
	}
}

// TestTranscriptIndex_ResolvesHashesRecordedByEarlierBuilds guards the
// consumer that actually persists hashes: a rewrite event stores the
// history it replaced by hash, and /rewind resolves those against the
// index. Both pre-change shapes must still resolve — a reasoning-free
// message (every journal older than persisted reasoning) and a
// reasoning-carrying one recorded while the hash still ignored its blocks.
func TestTranscriptIndex_ResolvesHashesRecordedByEarlierBuilds(t *testing.T) {
	plain := models.Message{Role: "user", Content: "u1"}
	reasoned := models.Message{
		Role:      "assistant",
		Content:   "running it",
		ToolCalls: []models.ToolCall{{ID: "call_1", Name: "run_command"}},
		Thinking:  thinkingBlocks("sig-A"),
	}
	// The digests an earlier build would have written into the rewrite
	// event: reasoning played no part in either.
	bare := reasoned
	bare.Thinking = nil
	recorded := []string{messageHash(plain), messageHash(bare)}

	idx := transcriptIndex([]transcriptEvent{
		{Kind: "msg", Message: &plain},
		{Kind: "msg", Message: &reasoned},
	})
	restored, ok := resolveHashes(idx, recorded)
	if !ok {
		t.Fatal("a restore point recorded before reasoning entered the hash must still resolve")
	}
	if len(restored) != 2 || len(restored[1].Thinking) != 1 || restored[1].Thinking[0].Signature != "sig-A" {
		t.Fatalf("the resolved message must carry its reasoning, got %+v", restored)
	}

	// An alias never outranks a real reasoning-free message with the same
	// bytes: the exact match wins.
	twin := bare
	idx = transcriptIndex([]transcriptEvent{
		{Kind: "msg", Message: &reasoned},
		{Kind: "msg", Message: &twin},
	})
	if got := idx[messageHash(bare)]; len(got.Thinking) != 0 {
		t.Fatal("an exact reasoning-free match must win over the alias of a reasoning-carrying twin")
	}
}
