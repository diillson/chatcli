/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package xai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestSendPrompt_SendsPromptCacheKey(t *testing.T) {
	var body map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": "ok"}}]}`))
	}))
	defer server.Close()
	c := NewXAIClient(testProvider("k"), "grok-4", zap.NewNop(), 1, 0)
	c.apiURL = server.URL
	history := []models.Message{{Role: "system", Content: "You are ChatCLI."}, {Role: "user", Content: "Hi"}}
	if _, err := c.SendPrompt(context.Background(), "Hi", history, 0); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	key, _ := body["prompt_cache_key"].(string)
	if key == "" || key != llmclient.PromptCacheKey(history) {
		t.Fatalf("prompt_cache_key = %q, want %q", key, llmclient.PromptCacheKey(history))
	}
	body = nil
	if _, err := c.SendPrompt(context.Background(), "Hi", []models.Message{{Role: "user", Content: "Hi"}}, 0); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if _, present := body["prompt_cache_key"]; present {
		t.Fatal("no system prompt → the field is omitted")
	}
}
