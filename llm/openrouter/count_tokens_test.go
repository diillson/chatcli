/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openrouter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/tokenizer"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// A GPT count either succeeds (warm vocabulary) or reports that the
// vocabulary is still loading — never anything else.
func assertGPTCount(t *testing.T, tc client.TokenCounter) {
	t.Helper()
	n, err := tc.CountTokens(context.Background(), "hello", []models.Message{{Role: "user", Content: "hi there"}})
	if err != nil && !errors.Is(err, tokenizer.ErrTokenizerLoading) {
		t.Fatalf("unexpected error: %v", err)
	}
	if err == nil && n <= 0 {
		t.Fatalf("warm count must be positive, got %d", n)
	}
}

func TestOpenRouterCountTokens_GPTLocal(t *testing.T) {
	c := NewOpenRouterClient(auth.NewStaticTokenProvider("k", auth.AuthModeAPIKey, auth.ProviderID("x")), "openai/gpt-5.6-terra", zap.NewNop(), 1, time.Millisecond)
	tc, ok := client.AsTokenCounter(c)
	if !ok {
		t.Fatal("client must expose TokenCounter")
	}
	assertGPTCount(t, tc)
	c.model = "claude-sonnet-5"
	if _, err := c.CountTokens(context.Background(), "x", nil); !errors.Is(err, tokenizer.ErrUnsupportedModel) {
		t.Fatalf("non-GPT models must report ErrUnsupportedModel, got %v", err)
	}
}
