package ollama

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestOllamaClient_SendPrompt_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(w, `{"message":{"role":"assistant","content":"Hello from Ollama!"},"done":true}`)
		if err != nil {
			return
		}
	}))
	defer srv.Close()

	logger, _ := zap.NewDevelopment()
	c := NewClient(srv.URL, "llama3.1:8b", logger, 1, 0)

	out, err := c.SendPrompt(context.Background(), "Hi", []models.Message{{Role: "user", Content: "Hi"}}, 0)
	require.NoError(t, err)
	assert.Equal(t, "Hello from Ollama!", out)
}

func TestOllamaClient_SendPrompt_RetryOnTemporaryError(t *testing.T) {
	attempt := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error": "Temporary error"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"Success on retry"},"done":true}`))
	}))
	defer srv.Close()

	logger, _ := zap.NewDevelopment()
	c := NewClient(srv.URL, "llama3.1:8b", logger, 2, 10*time.Millisecond)

	out, err := c.SendPrompt(context.Background(), "Test", []models.Message{{Role: "user", Content: "Test"}}, 0)
	require.NoError(t, err)
	assert.Equal(t, "Success on retry", out)
	assert.Equal(t, 2, attempt, "Should have made two attempts")
}

func TestFilterThinking(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Remove <thinking> tag",
			input:    "<thinking>Some reasoning here</thinking> Final Answer: Yes",
			expected: "Final Answer: Yes",
		},
		{
			name:     "Remove Thinking step by step",
			input:    "Thinking step by step: First, analyze... Final Answer: 42",
			expected: "Final Answer: 42",
		},
		{
			name:     "No change if no pattern",
			input:    "Direct answer: Hello",
			expected: "Direct answer: Hello",
		},
		{
			name:     "Case insensitive",
			input:    "<THINKING>Ignore this</THINKING> Response: OK",
			expected: "Response: OK",
		},
		{
			name:     "Remove <think> tag from log example",
			input:    "<think>\nOkay, the user said \"boa noite!\" which is Portuguese for \"good night!\" So they're probably greeting me in Portuguese. I should respond in Portuguese to be polite and show I understand. Let me make sure the response is friendly and offers help. Maybe say \"Boa noite! Como posso ajudar você hoje?\" That means \"Good night! How can I help you today?\" It's a common way to respond and keeps the conversation open. I should check if there's any cultural nuance I'm missing, but I think that's fine. Let's go with that.\n</think>\n\nBoa noite! Como posso ajudar você hoje? 😊",
			expected: "Boa noite! Como posso ajudar você hoje? 😊",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := filterThinking(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}
