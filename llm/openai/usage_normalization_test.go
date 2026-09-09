/*
 * ChatCLI - OpenAI usage schema contract
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestOpenAIUsageIsSubset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":22858,"completion_tokens":28,"total_tokens":22886,
			"prompt_tokens_details":{"cached_tokens":10515},
			"completion_tokens_details":{"reasoning_tokens":12}}}`))
	}))
	defer server.Close()
	t.Setenv("OPENAI_API_URL", server.URL)

	c := NewOpenAIClient(testProvider("test-api-key"), "gpt-6-astra", zap.NewNop(), 1, 0)
	if _, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 0); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	u := c.LastUsage()
	if u == nil || u.InputTokensTotal != 22858 || u.TotalTokens != 22886 {
		t.Fatalf("usage not normalized: %+v", u)
	}
}

// TestOpenAIToolPathUsageIsSubset covers the tool-calling path, which used to
// hand-roll its own usage block (dropping reasoning tokens) instead of going
// through the shared parser.
func TestOpenAIToolPathUsageIsSubset(t *testing.T) {
	resp, err := parseToolResponse(`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":900,"completion_tokens":40,"total_tokens":940,
		"prompt_tokens_details":{"cached_tokens":800},
		"completion_tokens_details":{"reasoning_tokens":16}}}`, zap.NewNop())
	if err != nil {
		t.Fatalf("parseToolResponse: %v", err)
	}
	if resp.Usage == nil || resp.Usage.InputTokensTotal != 900 {
		t.Fatalf("usage not normalized: %+v", resp.Usage)
	}
	if resp.Usage.ReasoningTokens != 16 {
		t.Fatalf("reasoning tokens dropped: %+v", resp.Usage)
	}
	if !resp.Usage.IsReal {
		t.Fatalf("tool-path usage must be marked real: %+v", resp.Usage)
	}
}
