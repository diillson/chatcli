/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func newTestTrimmerWithCompression() (*MessageTrimmer, *compress.Layer) {
	layer := compress.NewLayer(compress.Config{
		Mode:      compress.ModeLossyWithCCR,
		Store:     compress.NewMemoryStore(),
		Threshold: 100,
	})
	t := NewMessageTrimmer(zap.NewNop())
	t.SetCompressionLayer(layer)
	return t, layer
}

// big search-shaped body that the content router routes to the search compressor.
func bigSearchBody() string {
	var sb strings.Builder
	for f := 0; f < 6; f++ {
		for ln := 1; ln <= 15; ln++ {
			fmt.Fprintf(&sb, "pkg/file%d.go:%d:some matching content line %d here\n", f, ln, ln)
		}
	}
	return sb.String()
}

func TestTrimInjectedContextUsesReversibleCompression(t *testing.T) {
	trimmer, layer := newTestTrimmerWithCompression()
	content := "📦 CONTEXTO: attached.log\n\n" + bigSearchBody()

	msg := models.Message{Role: "user", Content: content}
	out := trimmer.TrimHistory([]models.Message{msg})
	got := out[0].Content

	if len(got) >= len(content) {
		t.Fatalf("injected context should shrink: %d >= %d", len(got), len(content))
	}
	if !strings.Contains(got, "📦 CONTEXTO:") {
		t.Fatal("the context header must be preserved")
	}
	keys := compress.ExtractKeys(got)
	if len(keys) == 0 {
		t.Fatal("compressed injected context must carry a reversible CCR marker (old path was lossy)")
	}
	if _, ok := layer.Recall(keys[0]); !ok {
		t.Fatal("the offloaded context must be recoverable via @recall")
	}
}

func TestTrimToolFeedbackRawBranchCompresses(t *testing.T) {
	trimmer, _ := newTestTrimmerWithCompression()
	// Tool feedback (matches isToolFeedbackMessage), >8000 chars, no <tool_output>.
	content := "--- Resultado da Ação 1 (@search) ---\n" + bigSearchBody()
	if len(content) <= 8000 {
		content += bigSearchBody()
	}
	msg := models.Message{Role: "user", Content: content}
	out := trimmer.TrimHistory([]models.Message{msg})
	got := out[0].Content
	if compress.ExtractKeys(got) == nil {
		t.Fatal("oversized raw tool feedback should be compressed reversibly")
	}
	if len(got) >= len(content) {
		t.Fatalf("expected reduction: %d >= %d", len(got), len(content))
	}
}

func TestTrimmerWithoutLayerFallsBack(t *testing.T) {
	// No layer set: behavior must be the legacy byte-truncation (no CCR marker).
	trimmer := NewMessageTrimmer(zap.NewNop())
	content := "📦 CONTEXTO: x\n\n" + bigSearchBody()
	out := trimmer.TrimHistory([]models.Message{{Role: "user", Content: content}})
	got := out[0].Content
	if compress.ExtractKeys(got) != nil {
		t.Fatal("without a layer there must be no CCR marker (pure fallback)")
	}
	if !strings.Contains(got, "omitted during compaction") {
		t.Fatal("fallback truncation marker expected")
	}
}

func TestTrimmerIdempotentOnCompressed(t *testing.T) {
	trimmer, _ := newTestTrimmerWithCompression()
	content := "📦 CONTEXTO: x\n\n" + bigSearchBody()
	first := trimmer.TrimHistory([]models.Message{{Role: "user", Content: content}})[0].Content
	second := trimmer.TrimHistory([]models.Message{{Role: "user", Content: first}})[0].Content
	if first != second {
		t.Fatal("re-trimming already-compressed content must be idempotent (no double offload)")
	}
}
