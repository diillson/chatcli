/*
 * ChatCLI - Ollama usage schema contract
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ollama

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// TestOllamaUsageIsWholeInput: Ollama reports no cache split, so
// prompt_eval_count already IS the input the model held.
func TestOllamaUsageIsWholeInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"hi"},"done":true,
			"prompt_eval_count":4321,"eval_count":77,"done_reason":"stop"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "llama3.1:8b", zap.NewNop(), 1, 0)
	if _, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 0); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}

	u := c.LastUsage()
	if u == nil || u.InputTokensTotal != 4321 || u.TotalTokens != 4398 {
		t.Fatalf("usage not normalized: %+v", u)
	}
}
