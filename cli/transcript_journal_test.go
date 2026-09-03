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
