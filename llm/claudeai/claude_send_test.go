package claudeai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

func TestClaudeClient_SendPrompt_APIKeyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "sk-test" {
			t.Errorf("x-api-key = %q, want sk-test", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Errorf("missing anthropic-version header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"Hello from Anthropic!"}]}`))
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	c := NewClaudeClient(
		auth.NewStaticTokenProvider("sk-test", auth.AuthModeAPIKey, auth.ProviderAnthropic),
		"claude-sonnet-4-20250514", logger, 1, 0,
	)
	c.apiURL = server.URL

	resp, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 100)
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if resp != "Hello from Anthropic!" {
		t.Errorf("response = %q, want Hello from Anthropic!", resp)
	}
}

func TestClaudeClient_SendPrompt_TokenPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer my-token" {
			t.Errorf("Authorization = %q, want Bearer my-token", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"token-mode-ok"}]}`))
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	c := NewClaudeClient(
		auth.NewStaticTokenProvider("my-token", auth.AuthModeToken, auth.ProviderAnthropic),
		"claude-sonnet-4-20250514", logger, 1, 0,
	)
	c.apiURL = server.URL

	resp, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 100)
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if resp != "token-mode-ok" {
		t.Errorf("response = %q", resp)
	}
}

func TestClaudeClient_SendPrompt_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	c := NewClaudeClient(
		auth.NewStaticTokenProvider("sk-test", auth.AuthModeAPIKey, auth.ProviderAnthropic),
		"claude-sonnet-4-20250514", logger, 1, 0,
	)
	c.apiURL = server.URL

	_, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 100)
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	var apiErr *utils.APIError
	if !errors.As(err, &apiErr) {
		t.Errorf("expected *utils.APIError, got %T", err)
	}
}

func TestClaudeClient_GetModelName(t *testing.T) {
	c := newTestClient()
	if c.GetModelName() == "" {
		t.Error("GetModelName empty")
	}
}

func TestClaudeClient_SupportsNativeTools(t *testing.T) {
	apiKey := newTestClient()
	if !apiKey.SupportsNativeTools() {
		t.Error("apiKey should support native tools")
	}
	oauth := &ClaudeClient{
		provider: auth.NewStaticTokenProvider("tok", auth.AuthModeOAuth, auth.ProviderAnthropic),
	}
	if oauth.SupportsNativeTools() {
		t.Error("oauth should NOT support native tools")
	}
}

func TestClaudeClient_SendPromptWithTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if _, ok := body["tools"]; !ok {
			t.Error("expected tools in request body")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content":[{"type":"text","text":"ok"}],
			"stop_reason":"end_turn"
		}`))
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	c := NewClaudeClient(
		auth.NewStaticTokenProvider("sk-test", auth.AuthModeAPIKey, auth.ProviderAnthropic),
		"claude-sonnet-4-20250514", logger, 1, 0,
	)
	c.apiURL = server.URL

	tools := []models.ToolDefinition{
		{
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "search",
				Description: "search the web",
				Parameters:  map[string]interface{}{"type": "object"},
			},
		},
	}

	llmResp, err := c.SendPromptWithTools(context.Background(), "search go",
		[]models.Message{{Role: "user", Content: "search go"}}, tools, 100)
	if err != nil {
		t.Fatalf("SendPromptWithTools: %v", err)
	}
	if llmResp == nil {
		t.Fatal("response is nil")
	}
}
