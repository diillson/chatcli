/*
 * ChatCLI - OpenRouter usage schema contract
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openrouter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/models"
)

// TestOpenRouterUsageIsSubset: OpenRouter speaks the OpenAI envelope for
// every vendor it fronts — including Anthropic models, whose cached tokens
// arrive as a SUBSET of prompt_tokens here, not beside them.
func TestOpenRouterUsageIsSubset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12000,"completion_tokens":90,"total_tokens":12090,
			"cost":0.0031,"prompt_tokens_details":{"cached_tokens":11000}}}`))
	}))
	defer server.Close()

	c := newTestClient(t, server.URL)
	if _, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 100); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}

	u := c.LastUsage()
	if u == nil || u.InputTokensTotal != 12000 || u.TotalTokens != 12090 {
		t.Fatalf("usage not normalized: %+v", u)
	}
}
