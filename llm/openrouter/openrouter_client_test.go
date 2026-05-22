package openrouter

import (
	"context"
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
	if _, ok := err.(*utils.APIError); !ok {
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
