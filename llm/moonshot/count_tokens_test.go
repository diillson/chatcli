/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package moonshot

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

func TestMoonshotCountTokens_EstimateEndpoint(t *testing.T) {
	var got map[string]interface{}
	var path, authz string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		authz = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"data":{"total_tokens": 77}}`))
	}))
	defer srv.Close()
	t.Setenv("MOONSHOT_API_URL", srv.URL+"/v1/chat/completions")
	c := NewMoonshotClient(auth.NewStaticTokenProvider("k", auth.AuthModeAPIKey, auth.ProviderID("moonshot")), "kimi-k2.6", zap.NewNop(), 1, time.Millisecond)
	tc, ok := client.AsTokenCounter(c)
	if !ok {
		t.Fatal("MoonshotClient must expose TokenCounter")
	}
	n, err := tc.CountTokens(context.Background(), "hi", []models.Message{{Role: "user", Content: "q"}})
	if err != nil || n != 77 {
		t.Fatalf("count=%d err=%v", n, err)
	}
	if path != "/v1/tokenizers/estimate-token-count" || authz != "Bearer k" || got["model"] != "kimi-k2.6" || got["messages"] == nil {
		t.Fatalf("path=%s authz=%q body=%v", path, authz, got)
	}
}

func TestMoonshotCountTokens_ErrorBodySurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"message":"bad model","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()
	t.Setenv("MOONSHOT_API_URL", srv.URL+"/v1/chat/completions")
	c := NewMoonshotClient(auth.NewStaticTokenProvider("k", auth.AuthModeAPIKey, auth.ProviderID("moonshot")), "kimi-k2.6", zap.NewNop(), 1, time.Millisecond)
	if _, err := c.CountTokens(context.Background(), "hi", nil); err == nil {
		t.Fatal("error envelope must surface")
	}
}
