/*
 * ChatCLI - structuredSummarize CCR archival tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */

package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// summarizerFake implements client.LLMClient and returns a canned summary.
type summarizerFake struct{}

func (summarizerFake) GetModelName() string { return "fake" }
func (summarizerFake) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	return "## Files Read\n- none of note\n## Current Task State\n- the user asked for a refactor of the parser and the assistant delivered it\n## Decisions\n- keep the lexer stateful", nil
}

func summarizeFixtureHistory(minKeepRecent int) []models.Message {
	h := []models.Message{{Role: "system", Content: "charter"}}
	// Middle block: 6 messages that will be summarized away.
	for i := 0; i < 3; i++ {
		h = append(h,
			models.Message{Role: "user", Content: strings.Repeat("question detail ", 50)},
			models.Message{Role: "assistant", Content: strings.Repeat("answer detail ", 50)},
		)
	}
	for i := 0; i < minKeepRecent; i++ {
		h = append(h, models.Message{Role: "user", Content: "recent"})
	}
	return h
}

func TestStructuredSummarize_ArchivesMiddleSegment(t *testing.T) {
	layer := compress.NewLayer(compress.Config{Mode: compress.ModeLossyWithCCR, Store: compress.NewMemoryStore()})
	hc := NewHistoryCompactor(zap.NewNop())
	hc.SetCompressionLayer(layer)

	cfg := CompactConfig{MinKeepRecent: 2}
	history := summarizeFixtureHistory(cfg.MinKeepRecent)

	got, _, err := hc.structuredSummarize(context.Background(), history, summarizerFake{}, cfg)
	if err != nil {
		t.Fatalf("structuredSummarize: %v", err)
	}

	var summary *models.Message
	for i := range got {
		if got[i].Meta != nil && got[i].Meta.IsSummary {
			summary = &got[i]
			break
		}
	}
	if summary == nil {
		t.Fatal("no summary message produced")
	}

	keys := compress.ExtractKeys(summary.Content)
	if len(keys) != 1 {
		t.Fatalf("summary must carry exactly one recall marker, got %d in %q", len(keys), summary.Content)
	}
	recovered, ok := layer.Recall(keys[0])
	if !ok {
		t.Fatal("archived segment not recoverable")
	}
	// The archive must carry the FULL middle segment, role-tagged.
	if !strings.Contains(recovered, "question detail") || !strings.Contains(recovered, "[assistant]:") {
		t.Errorf("archived segment incomplete: %q", recovered[:min(200, len(recovered))])
	}
}

func TestStructuredSummarize_NoLayerKeepsLegacyBehavior(t *testing.T) {
	hc := NewHistoryCompactor(zap.NewNop())
	cfg := CompactConfig{MinKeepRecent: 2}

	got, _, err := hc.structuredSummarize(context.Background(), summarizeFixtureHistory(cfg.MinKeepRecent), summarizerFake{}, cfg)
	if err != nil {
		t.Fatalf("structuredSummarize: %v", err)
	}
	for _, msg := range got {
		if msg.Meta != nil && msg.Meta.IsSummary {
			if compress.ExtractKeys(msg.Content) != nil {
				t.Error("without a compression layer the summary must not carry markers")
			}
			return
		}
	}
	t.Fatal("no summary message produced")
}
