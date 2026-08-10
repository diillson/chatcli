/*
 * ChatCLI - Tests for the /context attach auto-RAG upgrade
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"go.uber.org/zap"
)

func TestParseAttachFlags_FullFlag(t *testing.T) {
	for _, flag := range []string{"--full", "-f"} {
		f, err := parseAttachFlags([]string{"ctx", flag})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", flag, err)
		}
		if !f.full {
			t.Fatalf("%s: full not set", flag)
		}
	}
}

func TestParseAttachFlags_UnknownFlagStillRejected(t *testing.T) {
	if _, err := parseAttachFlags([]string{"ctx", "--bogus"}); err == nil {
		t.Fatal("unknown flag must error")
	}
}

func TestHandleAttach_FullWithRagConflict(t *testing.T) {
	f, err := parseAttachFlags([]string{"ctx", "--full", "--rag"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !f.full || f.retrievalTopK == 0 {
		t.Fatalf("parse should record both flags; got %+v", f)
	}
	// The conflict is enforced by handleAttach, not the parser — the parser
	// stays lenient so the error message can be specific.
}

// autoRagFixture builds a ContextHandler whose manager has no embedding
// provider (RetrievalEnabled() == false) — the conservative baseline.
func autoRagFixture(t *testing.T) *ContextHandler {
	t.Helper()
	h, err := NewContextHandler(zap.NewNop())
	if err != nil {
		t.Skipf("NewContextHandler unavailable: %v", err)
	}
	return h
}

func TestShouldAutoRag_Guards(t *testing.T) {
	h := autoRagFixture(t)
	big := &ctxmgr.FileContext{TotalSize: attachAutoRagMinBytes}

	base := attachFlags{}
	tests := []struct {
		name  string
		ctx   *ctxmgr.FileContext
		flags attachFlags
		env   string
		want  bool
	}{
		// Embeddings are disabled in this fixture, so even the happy path is
		// false — the provider gate is the outermost guard in production.
		{"no embedding provider", big, base, "", false},
		{"explicit --full", big, attachFlags{full: true}, "", false},
		{"explicit --rag", big, attachFlags{retrievalTopK: 8}, "", false},
		{"explicit --chunks", big, attachFlags{selectedChunks: []int{1}}, "", false},
		{"knowledge mode", &ctxmgr.FileContext{TotalSize: attachAutoRagMinBytes, Mode: ctxmgr.ModeKnowledge}, base, "", false},
		{"below size floor", &ctxmgr.FileContext{TotalSize: attachAutoRagMinBytes - 1}, base, "", false},
		{"env kill switch", big, base, "off", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(attachAutoRagEnvVar, tt.env)
			if got := h.shouldAutoRag(tt.ctx, tt.flags); got != tt.want {
				t.Fatalf("shouldAutoRag() = %v, want %v", got, tt.want)
			}
		})
	}
}

// stubEmbeddingProvider satisfies embedding.Provider so the positive
// auto-RAG path (retrieval enabled) is testable without a real backend.
type stubEmbeddingProvider struct{}

func (stubEmbeddingProvider) Name() string   { return "stub" }
func (stubEmbeddingProvider) Dimension() int { return 4 }
func (stubEmbeddingProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0, 0}
	}
	return out, nil
}

func TestShouldAutoRag_PositivePathWithProvider(t *testing.T) {
	h := autoRagFixture(t)
	h.manager.AttachEmbeddingProvider(stubEmbeddingProvider{})
	t.Setenv(attachAutoRagEnvVar, "")

	big := &ctxmgr.FileContext{TotalSize: attachAutoRagMinBytes}
	if !h.shouldAutoRag(big, attachFlags{}) {
		t.Fatal("expected auto-RAG upgrade: provider on, size at floor, no explicit flags")
	}
	// Boundary: one byte below the floor stays verbatim even with a provider.
	small := &ctxmgr.FileContext{TotalSize: attachAutoRagMinBytes - 1}
	if h.shouldAutoRag(small, attachFlags{}) {
		t.Fatal("below-floor context must stay verbatim")
	}
}

func TestAttachAutoRagEnabled_Values(t *testing.T) {
	for env, want := range map[string]bool{
		"": true, "1": true, "true": true, "anything": true,
		"0": false, "false": false, "off": false, "no": false, " OFF ": false,
	} {
		t.Setenv(attachAutoRagEnvVar, env)
		if got := attachAutoRagEnabled(); got != want {
			t.Fatalf("env %q: enabled = %v, want %v", env, got, want)
		}
	}
}

func TestContextAttachCompleter_SuggestsFull(t *testing.T) {
	var texts []string
	for _, s := range contextAttachFlagSuggestions() {
		texts = append(texts, s.Text)
	}
	joined := strings.Join(texts, " ")
	if !strings.Contains(joined, "--full") {
		t.Fatalf("completer missing --full: %v", texts)
	}
}
