package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestOpenAIClient_SendPromptWithTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tool-key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{
			"choices":[{
				"message":{"role":"assistant","content":"hi","tool_calls":[]},
				"finish_reason":"stop"
			}]
		}`))
	}))
	defer server.Close()

	originalURL := os.Getenv("OPENAI_API_URL")
	t.Setenv("OPENAI_API_URL", server.URL)
	t.Cleanup(func() { _ = os.Setenv("OPENAI_API_URL", originalURL) })

	logger, _ := zap.NewDevelopment()
	c := NewOpenAIClient(
		auth.NewStaticTokenProvider("tool-key", auth.AuthModeAPIKey, auth.ProviderOpenAI),
		"gpt-4o", logger, 1, 0,
	)

	tools := []models.ToolDefinition{
		{
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "search",
				Description: "find",
				Parameters:  map[string]interface{}{"type": "object"},
			},
		},
	}

	resp, err := c.SendPromptWithTools(context.Background(), "hi",
		[]models.Message{{Role: "user", Content: "hi"}}, tools, 100)
	if err != nil {
		t.Fatalf("SendPromptWithTools: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
}

func TestOpenAIClient_SupportsNativeTools(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	c := NewOpenAIClient(
		auth.NewStaticTokenProvider("k", auth.AuthModeAPIKey, auth.ProviderOpenAI),
		"gpt-4o", logger, 1, 0,
	)
	if !c.SupportsNativeTools() {
		t.Error("OpenAI client should support native tools")
	}
}
