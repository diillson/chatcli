package xai

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

func TestXAIClient_SendPrompt_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-xai-key", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		resp := `{"choices": [{"message": {"role": "assistant", "content": "Hello from xAI (Grok)!"}}]}`
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	client := NewXAIClient("test-xai-key", "grok-4", logger, 1, 0)
	client.apiURL = server.URL

	history := []models.Message{{Role: "user", Content: "Hi"}}
	resp, err := client.SendPrompt(context.Background(), "Hi", history, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Hello from xAI (Grok)!", resp)
}

func TestXAIClient_SendPrompt_RetryOnTemporaryError(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": {"message": "Rate limit exceeded"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": "Success on retry"}}]}`))
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	client := NewXAIClient("test-xai-key", "grok-4", logger, 2, 10*time.Millisecond)
	client.apiURL = server.URL

	resp, err := client.SendPrompt(context.Background(), "Test", []models.Message{{Role: "user", Content: "Test"}}, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Success on retry", resp)
	assert.Equal(t, 2, attempt, "Should have made two attempts")
}
