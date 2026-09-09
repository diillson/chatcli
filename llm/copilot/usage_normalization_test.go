/*
 * ChatCLI - GitHub Copilot usage schema contract
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package copilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCopilotUsageIsSubset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":2000,"completion_tokens":25,"total_tokens":2025,
			"prompt_tokens_details":{"cached_tokens":1800}}}`))
	}))
	defer server.Close()

	c := NewClient(testProvider("test-token"), "gpt-4o", testLogger(), 1, 0)
	c.baseURL = server.URL
	if _, err := c.SendPrompt(context.Background(), "Hello", nil, 0); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if u := c.LastUsage(); u == nil || u.InputTokensTotal != 2000 {
		t.Fatalf("usage not normalized: %+v", u)
	}
}
