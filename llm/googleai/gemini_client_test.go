package googleai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestGeminiClient_SendPrompt_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "key=test-api-key")
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		resp := `{"candidates": [{"content": {"parts": [{"text": "Hello from Gemini!"}]}}]}`
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	client := NewGeminiClient("test-api-key", "gemini-pro", logger, 1, 0)
	client.baseURL = server.URL

	history := []models.Message{{Role: "user", Content: "Hi"}}
	resp, err := client.SendPrompt(context.Background(), "Hi", history, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Hello from Gemini!", resp)
}

func TestGeminiClient_SendPrompt_RetryOnRateLimit(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": {"code": 429, "message": "Rate limit exceeded"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"candidates": [{"content": {"parts": [{"text": "Success on retry"}]}}]}`))
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	client := NewGeminiClient("test-api-key", "gemini-pro", logger, 2, 10*time.Millisecond)
	client.baseURL = server.URL

	resp, err := client.SendPrompt(context.Background(), "Test", []models.Message{{Role: "user", Content: "Test"}}, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Success on retry", resp)
	assert.Equal(t, 2, attempt, "Should have made two attempts")
}
