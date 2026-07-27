package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func testProvider(key string) auth.TokenProvider {
	return auth.NewStaticTokenProvider(key, auth.AuthModeAPIKey, "")
}

func TestOpenAIClient_SendPrompt_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		resp := `{"choices": [{"message": {"role": "assistant", "content": "Hello from OpenAI!"}}]}`
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	client := NewOpenAIClient(testProvider("test-api-key"), "gpt-4o", logger, 1, 0)

	// Injetar a URL do servidor mock
	// Precisamos refatorar o cliente para permitir isso, como fizemos com Claude.
	// Vamos assumir que a refatoração foi feita.
	originalURL := utils.GetEnvOrDefault("OPENAI_API_URL", "")
	os.Setenv("OPENAI_API_URL", server.URL)
	defer os.Setenv("OPENAI_API_URL", originalURL)

	history := []models.Message{{Role: "user", Content: "Hi"}}
	resp, err := client.SendPrompt(context.Background(), "Hi", history, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Hello from OpenAI!", resp)
}

func TestOpenAIClient_ListModels_CustomEndpointNoFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "gpt-5.4", "owned_by": "openai"},
				{"id": "claude-opus-5", "owned_by": "anthropic"},
				{"id": "gemini-3-pro", "owned_by": "google"}
			]
		}`))
	}))
	defer server.Close()

	os.Setenv("OPENAI_API_URL", server.URL+"/chat/completions")
	defer os.Unsetenv("OPENAI_API_URL")

	logger, _ := zap.NewDevelopment()
	client := NewOpenAIClient(testProvider("test-api-key"), "gpt-5.4", logger, 1, 0)
	list, err := client.ListModels(context.Background())

	assert.NoError(t, err)
	// custom endpoint (gateway) → no family filter, all models listed
	assert.Len(t, list, 3)
}

func TestKeepModel_OfficialEndpointFiltersFamilies(t *testing.T) {
	assert.True(t, keepModel("gpt-5.4", false))
	assert.True(t, keepModel("o1-preview", false))
	assert.True(t, keepModel("o3-mini", false))
	assert.True(t, keepModel("o4-mini", false))
	assert.True(t, keepModel("chatgpt-4o-latest", false))
	assert.False(t, keepModel("text-embedding-3", false))
	assert.False(t, keepModel("whisper-1", false))
	assert.False(t, keepModel("dall-e-3", false))
	assert.False(t, keepModel("claude-opus-5", false))

	// custom endpoint keeps everything
	assert.True(t, keepModel("text-embedding-3", true))
	assert.True(t, keepModel("claude-opus-5", true))
}
