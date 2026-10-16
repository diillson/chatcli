package xai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func testProvider(key string) auth.TokenProvider {
	return auth.NewStaticTokenProvider(key, auth.AuthModeAPIKey, "")
}

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
	client := NewXAIClient(testProvider("test-xai-key"), "grok-4", logger, 1, 0)
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
	client := NewXAIClient(testProvider("test-xai-key"), "grok-4", logger, 2, 10*time.Millisecond)
	client.apiURL = server.URL

	resp, err := client.SendPrompt(context.Background(), "Test", []models.Message{{Role: "user", Content: "Test"}}, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Success on retry", resp)
	assert.Equal(t, 2, attempt, "Should have made two attempts")
}

func TestXAIClient_ListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-xai-key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-4","owned_by":"xai"}]}`))
	}))
	defer server.Close()
	logger, _ := zap.NewDevelopment()
	client := NewXAIClient(testProvider("test-xai-key"), "grok-4", logger, 1, 0)
	client.apiURL = server.URL + "/chat/completions"
	list, err := client.ListModels(context.Background())
	assert.NoError(t, err)
	assert.NotEmpty(t, list)
}

func TestXAIClient_SendPrompt_SendsPromptOutsideHistory(t *testing.T) {
	var got struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": "ok"}}]}`))
	}))
	defer server.Close()

	client := NewXAIClient(testProvider("k"), "grok-4", zap.NewNop(), 1, 0)
	client.apiURL = server.URL

	cases := []struct {
		name    string
		history []models.Message
		want    []string
	}{
		{"system only", []models.Message{{Role: "system", Content: "sys"}}, []string{"system:sys", "user:review this"}},
		{"no history", nil, []string{"user:review this"}},
		{"already last", []models.Message{{Role: "user", Content: "review this"}}, []string{"user:review this"}},
		{"after assistant", []models.Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}}, []string{"user:q", "assistant:a", "user:review this"}},
	}
	for _, c := range cases {
		_, err := client.SendPrompt(context.Background(), "review this", c.history, 0)
		assert.NoError(t, err, c.name)
		var roles []string
		for _, m := range got.Messages {
			roles = append(roles, m.Role+":"+m.Content)
		}
		assert.Equal(t, c.want, roles, c.name)
	}
}
