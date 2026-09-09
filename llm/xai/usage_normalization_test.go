/*
 * ChatCLI - xAI usage schema contract
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package xai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// TestXAIUsageIsSubset: Grok speaks the OpenAI envelope — cached_tokens is
// inside prompt_tokens, and the long-context tier must be judged on that
// figure alone, never on prompt+cached.
func TestXAIUsageIsSubset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":150000,"completion_tokens":40,"total_tokens":150040,
			"prompt_tokens_details":{"cached_tokens":120000}}}`))
	}))
	defer server.Close()

	c := NewXAIClient(testProvider("test-xai-key"), "grok-4.6", zap.NewNop(), 1, 0)
	c.apiURL = server.URL
	if _, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 0); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if u := c.LastUsage(); u == nil || u.InputTokensTotal != 150000 {
		t.Fatalf("usage not normalized: %+v", u)
	}
}
