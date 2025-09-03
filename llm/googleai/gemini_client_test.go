package googleai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestGeminiClient_SendPrompt_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "key=test-api-key")
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		resp := `{
                        "candidates": [{
                                "content": {
                                        "parts": [{"text": "Hello from Gemini!"}]
                                }
                        }]
                }`
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	client := NewGeminiClient("test-api-key", "gemini-pro", logger, 1, 0)

	// Injetar a URL base do servidor mock
	client.baseURL = server.URL

	history := []models.Message{{Role: "user", Content: "Hi"}}
	resp, err := client.SendPrompt(context.Background(), "Hi", history)

	assert.NoError(t, err)
	assert.Equal(t, "Hello from Gemini!", resp)
}
