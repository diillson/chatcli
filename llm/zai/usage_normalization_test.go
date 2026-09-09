/*
 * ChatCLI - Z.AI usage schema contract
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package zai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestZAIUsageIsSubset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":8000,"completion_tokens":60,"total_tokens":8060,
			"prompt_tokens_details":{"cached_tokens":7000}}}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	if _, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 0); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if u := c.LastUsage(); u == nil || u.InputTokensTotal != 8000 {
		t.Fatalf("usage not normalized: %+v", u)
	}
}
