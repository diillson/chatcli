/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package zai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestZAICountTokens_TokenizerEndpoint(t *testing.T) {
	var got map[string]interface{}
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":"1","usage":{"prompt_tokens": 55, "total_tokens": 55}}`))
	}))
	defer srv.Close()
	t.Setenv("ZAI_API_URL", srv.URL+"/api/paas/v4/chat/completions")
	c := NewZAIClient(context.Background(), auth.NewStaticTokenProvider("k", auth.AuthModeAPIKey, auth.ProviderID("zai")), "glm-5.3", zap.NewNop(), 1, time.Millisecond)
	tc, ok := client.AsTokenCounter(c)
	if !ok {
		t.Fatal("ZAIClient must expose TokenCounter")
	}
	n, err := tc.CountTokens(context.Background(), "hi", []models.Message{{Role: "user", Content: "q"}})
	if err != nil || n != 55 {
		t.Fatalf("count=%d err=%v", n, err)
	}
	if path != "/api/paas/v4/tokenizer" || got["model"] != "glm-5.3" || got["messages"] == nil {
		t.Fatalf("path=%s body=%v", path, got)
	}
}
