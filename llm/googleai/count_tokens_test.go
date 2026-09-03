/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package googleai

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

func TestGeminiCountTokens_UsesGenerateContentRequestShape(t *testing.T) {
	var got map[string]interface{}
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"totalTokens": 321}`))
	}))
	defer srv.Close()
	c := NewGeminiClient(auth.NewStaticTokenProvider("k", auth.AuthModeAPIKey, auth.ProviderID("googleai")), "gemini-2.5-flash", zap.NewNop(), 1, time.Millisecond)
	c.baseURL = srv.URL
	tc, ok := client.AsTokenCounter(c)
	if !ok {
		t.Fatal("GeminiClient must expose TokenCounter")
	}
	n, err := tc.CountTokens(context.Background(), "hi", []models.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "q"}})
	if err != nil || n != 321 {
		t.Fatalf("count=%d err=%v", n, err)
	}
	if !strings.HasSuffix(path, "/models/gemini-2.5-flash:countTokens") {
		t.Fatalf("path=%s", path)
	}
	gen, _ := got["generateContentRequest"].(map[string]interface{})
	if gen == nil || gen["contents"] == nil || gen["system_instruction"] == nil || gen["model"] != "models/gemini-2.5-flash" {
		t.Fatalf("request must wrap the generate shape: %v", got)
	}
}
