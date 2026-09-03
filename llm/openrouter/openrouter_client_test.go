package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

func testProvider(key string) auth.TokenProvider {
	return auth.NewStaticTokenProvider(key, auth.AuthModeAPIKey, "")
}

func newTestClient(t *testing.T, url string) *OpenRouterClient {
	t.Helper()
	logger, _ := zap.NewDevelopment()
	c := NewOpenRouterClient(testProvider("test-router-key"), "anthropic/claude-3-haiku", logger, 1, 0)
	t.Setenv("OPENROUTER_API_URL", url)
	return c
}

func TestOpenRouterClient_SendPrompt_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-router-key" {
			t.Errorf("Authorization = %q, want Bearer test-router-key", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Hello from OpenRouter!"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	c := newTestClient(t, server.URL)
	resp, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 100)
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if resp != "Hello from OpenRouter!" {
		t.Errorf("response = %q", resp)
	}
}

func TestOpenRouterClient_SendPrompt_ForwardsExtraHeaders(t *testing.T) {
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	t.Setenv("OPENROUTER_HTTP_REFERER", "https://chatcli.example/")
	t.Setenv("OPENROUTER_APP_TITLE", "chatcli-test")

	c := newTestClient(t, server.URL)
	if _, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 100); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if receivedHeaders.Get("HTTP-Referer") != "https://chatcli.example/" {
		t.Errorf("HTTP-Referer = %q", receivedHeaders.Get("HTTP-Referer"))
	}
	if receivedHeaders.Get("X-Title") != "chatcli-test" {
		t.Errorf("X-Title = %q", receivedHeaders.Get("X-Title"))
	}
}

func TestOpenRouterClient_SendPrompt_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad","code":400}}`))
	}))
	defer server.Close()

	c := newTestClient(t, server.URL)
	_, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 100)
	if err == nil {
		t.Fatal("expected error for 400")
	}
	var apiErr *utils.APIError
	if !errors.As(err, &apiErr) {
		t.Errorf("expected *utils.APIError, got %T", err)
	}
}

func TestOpenRouterClient_GetModelName(t *testing.T) {
	c := newTestClient(t, "http://localhost")
	if c.GetModelName() == "" {
		t.Error("empty model name")
	}
}

func TestOpenRouterClient_GetAPIURL_Default(t *testing.T) {
	_ = os.Unsetenv("OPENROUTER_API_URL")
	logger, _ := zap.NewDevelopment()
	c := NewOpenRouterClient(testProvider("k"), "m", logger, 1, 0)
	if got := c.getAPIURL(); got == "" {
		t.Error("getAPIURL returned empty")
	}
}

func TestOpenRouterClient_ListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"anthropic/claude-3-haiku","name":"Claude 3 Haiku","context_length":200000}]}`))
	}))
	defer server.Close()
	c := newTestClient(t, server.URL+"/chat/completions")
	list, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("got %d models, want 1", len(list))
	}
}

func TestOpenRouterCacheControl_OnlyForCachingVendors(t *testing.T) {
	if !openRouterSupportsCacheControl("anthropic/claude-sonnet-5") || !openRouterSupportsCacheControl("google/gemini-2.5-pro") {
		t.Fatal("anthropic/ and google/ models honor cache_control")
	}
	if openRouterSupportsCacheControl("openai/gpt-5.6") || openRouterSupportsCacheControl("deepseek/deepseek-chat") {
		t.Fatal("automatic-caching vendors must not receive the marker")
	}
}

func TestApplyOpenRouterCacheControl_MarksSystemAndLastUser(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "system", "content": "stable prefix"},
		{"role": "user", "content": "first"},
		{"role": "assistant", "content": "ok"},
		{"role": "tool", "content": "result", "tool_call_id": "t1"},
		{"role": "user", "content": []interface{}{
			map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": "data:..."}},
			map[string]interface{}{"type": "text", "text": "what is it"},
		}},
	}
	applyOpenRouterCacheControl(messages)

	sys := messages[0]["content"].([]map[string]interface{})
	if len(sys) != 1 || sys[0]["cache_control"] == nil || sys[0]["text"] != "stable prefix" {
		t.Fatalf("system not converted+marked: %#v", messages[0]["content"])
	}
	if _, isStr := messages[1]["content"].(string); !isStr {
		t.Fatal("earlier user turns must stay untouched")
	}
	if _, isStr := messages[3]["content"].(string); !isStr {
		t.Fatal("tool messages must stay untouched")
	}
	parts := messages[4]["content"].([]interface{})
	if parts[1].(map[string]interface{})["cache_control"] == nil {
		t.Fatal("last user text part must be marked")
	}
	if parts[0].(map[string]interface{})["cache_control"] != nil {
		t.Fatal("image part must not be marked")
	}
}

// The real builder wraps content in the vision wire type; the marker must
// see through it on both the system and the last user message.
func TestBuildMessages_CacheControlOnRealWireContent(t *testing.T) {
	c := &OpenRouterClient{model: "anthropic/claude-sonnet-5"}
	history := []models.Message{
		{Role: "system", Content: "stable prefix"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "second"},
	}
	messages := c.buildMessages("second", history)
	sys, ok := messages[0]["content"].([]map[string]interface{})
	if !ok || sys[0]["cache_control"] == nil {
		t.Fatalf("system content not marked: %#v", messages[0]["content"])
	}
	last, ok := messages[len(messages)-1]["content"].([]map[string]interface{})
	if !ok || last[0]["cache_control"] == nil || last[0]["text"] != "second" {
		t.Fatalf("last user content not marked: %#v", messages[len(messages)-1]["content"])
	}
	if raw, _ := json.Marshal(messages[1]["content"]); string(raw) != `"first"` {
		t.Fatalf("earlier user turn must keep its wire bytes, got %s", raw)
	}

	// Non-caching vendor: bytes unchanged.
	plain := &OpenRouterClient{model: "openai/gpt-5.6"}
	pm := plain.buildMessages("second", history)
	if raw, _ := json.Marshal(pm[0]["content"]); string(raw) != `"stable prefix"` {
		t.Fatalf("openai/ must not receive markers, got %s", raw)
	}
}
