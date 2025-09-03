package openai_responses

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestOpenAIResponsesClient_SendPrompt_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"output_text": "Hello from Responses API!"}`)
	}))
	defer server.Close()

	// Sobrescrever a URL da API via variável de ambiente
	originalURL := os.Getenv("OPENAI_RESPONSES_API_URL")
	require.NoError(t, os.Setenv("OPENAI_RESPONSES_API_URL", server.URL))
	defer os.Setenv("OPENAI_RESPONSES_API_URL", originalURL)

	logger, _ := zap.NewDevelopment()
	client := NewOpenAIResponsesClient("test-api-key", "gpt-5", logger, 1, 0)

	history := []models.Message{{Role: "user", Content: "Hi"}}
	resp, err := client.SendPrompt(context.Background(), "Hi", history)

	assert.NoError(t, err)
	assert.Equal(t, "Hello from Responses API!", resp)
}

func TestOpenAIResponsesClient_buildTextFromHistory(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: "Be helpful."},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
	}
	prompt := "How are you?"

	expected := "System: Be helpful.\nUser: Hello\nAssistant: Hi there!\nUser: How are you?"
	result := buildTextFromHistory(history, prompt)
	assert.Equal(t, expected, result)
}
