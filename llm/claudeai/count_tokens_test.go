/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestClaudeCountTokens_PostsCountEndpointWithoutGenerationFields(t *testing.T) {
	var got map[string]interface{}
	var path, apiKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		apiKey = r.Header.Get("x-api-key")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"input_tokens": 1234}`))
	}))
	defer srv.Close()
	c := NewClaudeClient(auth.NewStaticTokenProvider("k", auth.AuthModeAPIKey, auth.ProviderID("claudeai")), "claude-sonnet-5", zap.NewNop(), 1, time.Millisecond)
	c.apiURL = srv.URL + "/v1/messages"

	tc, ok := client.AsTokenCounter(c)
	if !ok {
		t.Fatal("ClaudeClient must expose TokenCounter")
	}
	n, err := tc.CountTokens(context.Background(), "hello", []models.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}})
	if err != nil || n != 1234 {
		t.Fatalf("count=%d err=%v", n, err)
	}
	if !strings.HasSuffix(path, "/v1/messages/count_tokens") || apiKey != "k" {
		t.Fatalf("path=%s key=%q", path, apiKey)
	}
	for _, forbidden := range []string{"max_tokens", "stream", "thinking"} {
		if _, present := got[forbidden]; present {
			t.Fatalf("count request must not carry %s", forbidden)
		}
	}
	if got["model"] != "claude-sonnet-5" || got["system"] == nil || got["messages"] == nil {
		t.Fatalf("count request must mirror the send shape: %v", got)
	}
}

func TestClaudeCountTokens_ErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	defer srv.Close()
	c := NewClaudeClient(auth.NewStaticTokenProvider("k", auth.AuthModeAPIKey, auth.ProviderID("claudeai")), "claude-sonnet-5", zap.NewNop(), 1, time.Millisecond)
	c.apiURL = srv.URL + "/v1/messages"
	if _, err := c.CountTokens(context.Background(), "x", nil); err == nil {
		t.Fatal("HTTP errors must surface so callers fall back")
	}
}
