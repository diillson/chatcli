package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

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
	client := NewOpenAIClient("test-api-key", "gpt-4o", logger, 1, 0)

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
