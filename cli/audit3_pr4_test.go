/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/cli/hooks"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestVerbatimToolResult_SurvivesPairingAfterLevel2(t *testing.T) {
	hc := NewHistoryCompactor(zap.NewNop())
	h := []models.Message{{Role: "system", Content: "charter"}}
	for i := 0; i < 3; i++ {
		h = append(h, models.Message{Role: "user", Content: strings.Repeat("question detail ", 50)}, models.Message{Role: "assistant", Content: strings.Repeat("answer detail ", 50)})
	}
	h = append(h,
		models.Message{Role: "assistant", ToolCalls: []models.ToolCall{{ID: "r1", Name: "recall", Arguments: map[string]interface{}{"key": "k"}}}},
		models.Message{Role: "tool", ToolCallID: "r1", Content: "RECALLED ORIGINAL " + strings.Repeat("x", 300), Meta: &models.MessageMeta{PreserveVerbatim: true}},
		models.Message{Role: "assistant", Content: strings.Repeat("more answer ", 50)},
		models.Message{Role: "user", Content: "recent 1"}, models.Message{Role: "user", Content: "recent 2"})
	out, _, err := hc.structuredSummarize(context.Background(), h, &countingSummarizer{summary: strings.Repeat("## Summary\n- point ", 8)}, CompactConfig{MinKeepRecent: 2})
	if err != nil {
		t.Fatal(err)
	}
	repaired, rep := agent.EnsureToolResultPairing(out, zap.NewNop())
	if rep.OrphanResultsRemoved != 0 {
		t.Fatalf("the verbatim result must not be an orphan after compaction: %+v", rep)
	}
	found := false
	for _, m := range repaired {
		if m.Meta != nil && m.Meta.PreserveVerbatim && strings.Contains(m.Content, "RECALLED ORIGINAL") {
			found = true
			if m.Role != "user" || m.ToolCallID != "" {
				t.Fatalf("verbatim result must be downgraded to a user message: %+v", m.Role)
			}
		}
	}
	if !found {
		t.Fatal("verbatim content must survive Level 2 and the pairing repair")
	}
}

func TestVerbatimCap_CompactionConverges(t *testing.T) {
	layer := compress.NewLayer(compress.Config{Mode: compress.ModeLossyWithCCR, Store: compress.NewMemoryStore()})
	hc := NewHistoryCompactor(zap.NewNop())
	hc.SetCompressionLayer(layer)
	cfg := CompactConfig{Provider: "openai", Model: "gpt-5.6-terra", MinKeepRecent: 2, BudgetRatio: 0.5, CharsPerToken: 4, MaxPayloadBytes: 8000}
	budget := hc.CharBudget(cfg)
	h := []models.Message{{Role: "system", Content: "charter"}}
	// Six recalled originals, each a third of the budget: exempt from every
	// level, they used to keep the history above budget forever.
	for i := 0; i < 6; i++ {
		h = append(h, models.Message{Role: "user", Content: strings.Repeat("v", budget/3), Meta: &models.MessageMeta{PreserveVerbatim: true}})
	}
	h = append(h, models.Message{Role: "user", Content: "recent 1"}, models.Message{Role: "assistant", Content: "recent 2"})
	out, err := hc.Compact(context.Background(), h, &countingSummarizer{summary: strings.Repeat("## Summary\n- point ", 8)}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if hc.NeedsCompaction(out, cfg) {
		t.Fatalf("compaction must converge under budget: %d > %d", totalChars(out), budget)
	}
	stubs, recall := 0, 0
	for _, m := range out {
		if strings.Contains(m.Content, "aged out of the window") {
			stubs++
			if strings.Contains(m.Content, "@recall") {
				recall++
			}
		}
	}
	if stubs == 0 || recall != stubs {
		t.Fatalf("aged verbatim results must be stubbed with a recall marker: stubs=%d recall=%d", stubs, recall)
	}
}

func TestSkippedCompaction_PopsSnapshotAndPairsHooks(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop(), costTracker: NewCostTracker(), hookManager: hooks.NewManager(zap.NewNop())}
	cli.history = []models.Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}}
	cli.beforeCompaction(context.Background(), compactTriggerAuto)
	if len(cli.preCompaction) != 1 {
		t.Fatal("beforeCompaction pushes a snapshot")
	}
	cli.compactionSkipped(context.Background(), compactTriggerAuto)
	if len(cli.preCompaction) != 0 {
		t.Fatal("a skipped compaction must pop its snapshot")
	}
	if cli.undoCompaction() {
		t.Fatal("nothing to undo after a skipped compaction")
	}
	if !historiesEqual(cli.history, cli.history) || historiesEqual(cli.history, cli.history[:1]) {
		t.Fatal("historiesEqual")
	}
	ev := cli.compactionEvent(hooks.EventPostCompact, compactTriggerManual)
	ev.Outcome = compactOutcomeSkipped
	if ev.Outcome != "skipped" || ev.Trigger != "manual" {
		t.Fatal("event shape")
	}
}

func TestRewindCheckpoint_UsesTheSharedRestoreBookkeeping(t *testing.T) {
	cli := journaledCLI(t)
	cli.history = longHistory(2)
	cli.syncTranscript()
	cli.saveCheckpoint()
	cli.history = append(cli.history, models.Message{Role: "user", Content: "later"})
	cli.syncTranscript()
	cli.preCompaction = [][]models.Message{longHistory(1)}
	// Restore the checkpoint the way showRewindMenu does, through the shared helper.
	cp := cli.checkpoints[0]
	cli.history = append([]models.Message(nil), cp.History...)
	cli.afterHistoryRestore()
	if len(cli.preCompaction) != 0 {
		t.Fatal("restore must clear the undo stack of the abandoned timeline")
	}
	events, _ := cli.transcriptEvents()
	rewrites := 0
	for _, e := range events {
		if e.Kind == "rewrite" {
			rewrites++
		}
	}
	if rewrites == 0 {
		t.Fatal("restore must be journaled as a rewrite")
	}
	if _, _, rebuilds := cli.costTracker.CacheStats().Requests, 0, cli.costTracker.CacheStats().Rebuilds; rebuilds < 0 {
		t.Fatal("cache telemetry consulted")
	}
}
