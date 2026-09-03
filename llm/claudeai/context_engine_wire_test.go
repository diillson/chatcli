/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diillson/chatcli/auth"
	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func captureToolRequest(t *testing.T) (map[string]interface{}, http.Header) {
	t.Helper()
	var body map[string]interface{}
	var headers http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		headers = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1},"context_management":{"applied_edits":[{"type":"clear_tool_uses_20250919","cleared_tool_uses":3,"cleared_input_tokens":12000}]}}`))
	}))
	defer server.Close()
	c := NewClaudeClient(auth.NewStaticTokenProvider("sk-test", auth.AuthModeAPIKey, auth.ProviderAnthropic), "claude-sonnet-5", zap.NewNop(), 1, 0)
	c.apiURL = server.URL
	tools := []models.ToolDefinition{{Type: "function", Function: models.ToolFunctionDef{Name: "read_file", Description: "read", Parameters: map[string]interface{}{"type": "object"}}}}
	resp, err := c.SendPromptWithTools(context.Background(), "go", []models.Message{{Role: "user", Content: "go"}}, tools, 100)
	if err != nil || resp == nil {
		t.Fatalf("SendPromptWithTools: %v", err)
	}
	return body, headers
}

func TestProviderContextEngine_AddsContextManagementAndBeta(t *testing.T) {
	t.Setenv(llmclient.ContextEngineEnv, "provider")
	t.Setenv(llmclient.PromptCacheTTLEnv, "1h")
	body, headers := captureToolRequest(t)
	cm, ok := body["context_management"].(map[string]interface{})
	if !ok {
		t.Fatalf("context_management missing: %v", body)
	}
	edits, _ := cm["edits"].([]interface{})
	if len(edits) != 1 || edits[0].(map[string]interface{})["type"] != "clear_tool_uses_20250919" {
		t.Fatalf("edits = %v", edits)
	}
	beta := headers.Get("anthropic-beta")
	if !strings.Contains(beta, llmclient.ContextManagementBeta) || !strings.Contains(beta, llmclient.ExtendedCacheTTLBeta) {
		t.Fatalf("beta header = %q", beta)
	}
	// OAuth path carries the beta too.
	req, _ := http.NewRequest(http.MethodPost, "http://x", nil)
	applyOAuthHeaders(req, "tok")
	if !strings.Contains(req.Header.Get("anthropic-beta"), llmclient.ContextManagementBeta) {
		t.Fatalf("oauth beta header = %q", req.Header.Get("anthropic-beta"))
	}
	// Adding the same beta twice does not duplicate it.
	addAnthropicBeta(req, llmclient.ContextManagementBeta)
	if strings.Count(req.Header.Get("anthropic-beta"), llmclient.ContextManagementBeta) != 1 {
		t.Fatal("beta must not be duplicated")
	}
}

func TestProviderContextEngine_OffByDefault(t *testing.T) {
	t.Setenv(llmclient.ContextEngineEnv, "")
	t.Setenv(llmclient.PromptCacheTTLEnv, "")
	body, headers := captureToolRequest(t)
	if _, ok := body["context_management"]; ok {
		t.Fatal("context_management must be absent when the engine is builtin")
	}
	if strings.Contains(headers.Get("anthropic-beta"), llmclient.ContextManagementBeta) {
		t.Fatal("beta must be absent when the engine is builtin")
	}
}
