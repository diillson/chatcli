/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openai

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

func TestSendPrompt_PromptCacheRetentionByModelAndPreference(t *testing.T) {
	var body map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": "ok"}}]}`))
	}))
	defer server.Close()
	t.Setenv("OPENAI_API_URL", server.URL)
	history := []models.Message{{Role: "system", Content: "You are ChatCLI."}, {Role: "user", Content: "Hi"}}

	send := func(model string) map[string]interface{} {
		body = nil
		c := NewOpenAIClient(testProvider("test-api-key"), model, zap.NewNop(), 1, 0)
		if _, err := c.SendPrompt(context.Background(), "Hi", history, 0); err != nil {
			t.Fatalf("SendPrompt: %v", err)
		}
		return body
	}
	t.Setenv(llmclient.PromptCacheTTLEnv, "1h")
	if got := send("gpt-5.5")["prompt_cache_retention"]; got != "24h" {
		t.Fatalf("gpt-5.5 with the extended preference must send 24h, got %#v", got)
	}
	if _, present := send("gpt-5.6")["prompt_cache_retention"]; present {
		t.Fatal("gpt-5.6+ deprecates the field (prompt_cache_options has a single ttl); nothing must be sent")
	}
	if _, present := send("gpt-4.1")["prompt_cache_retention"]; present {
		t.Fatal("older models must not receive the field")
	}
	t.Setenv(llmclient.PromptCacheTTLEnv, "5m")
	if _, present := send("gpt-5.5")["prompt_cache_retention"]; present {
		t.Fatal("default lifetime sends nothing")
	}
}
