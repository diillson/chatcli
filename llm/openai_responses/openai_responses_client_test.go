package openai_responses

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func testProvider(key string) auth.TokenProvider {
	return auth.NewStaticTokenProvider(key, auth.AuthModeAPIKey, "")
}

func TestOpenAIResponsesClient_SendPrompt_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"output_text": "Hello from Responses API!"}`)
	}))
	defer server.Close()

	originalURL := os.Getenv("OPENAI_RESPONSES_API_URL")
	require.NoError(t, os.Setenv("OPENAI_RESPONSES_API_URL", server.URL))
	defer os.Setenv("OPENAI_RESPONSES_API_URL", originalURL)

	logger, _ := zap.NewDevelopment()
	client := NewOpenAIResponsesClient(testProvider("test-api-key"), "gpt-5", logger, 1, 0)

	history := []models.Message{{Role: "user", Content: "Hi"}}
	resp, err := client.SendPrompt(context.Background(), "Hi", history, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Hello from Responses API!", resp)
}

func TestOpenAIResponsesClient_SendPrompt_RetryOnTemporaryError(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error": {"message": "Rate limit exceeded"}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"output_text": "Success on retry!"}`)
	}))
	defer server.Close()

	originalURL := os.Getenv("OPENAI_RESPONSES_API_URL")
	require.NoError(t, os.Setenv("OPENAI_RESPONSES_API_URL", server.URL))
	defer os.Setenv("OPENAI_RESPONSES_API_URL", originalURL)

	logger, _ := zap.NewDevelopment()
	client := NewOpenAIResponsesClient(testProvider("test-api-key"), "gpt-5", logger, 2, 10*time.Millisecond)

	history := []models.Message{{Role: "user", Content: "Test"}}
	resp, err := client.SendPrompt(context.Background(), "Test", history, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Success on retry!", resp)
	assert.Equal(t, 2, attempt, "Should have made two attempts")
}

func TestOpenAIResponsesClient_ListModels_APIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"data":[{"id":"gpt-5"},{"id":"text-embedding-3"}]}`)
	}))
	defer server.Close()
	require.NoError(t, os.Setenv("OPENAI_API_URL", server.URL+"/chat/completions"))
	defer os.Unsetenv("OPENAI_API_URL")

	logger, _ := zap.NewDevelopment()
	client := NewOpenAIResponsesClient(testProvider("test-api-key"), "gpt-5", logger, 1, 0)
	list, err := client.ListModels(context.Background())
	assert.NoError(t, err)
	// only gpt-* models pass the prefix filter
	assert.Equal(t, 1, len(list))
}

func TestOpenAIResponsesClient_ListModels_OAuthSkips(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	client := NewOpenAIResponsesClient(
		auth.NewStaticTokenProvider("token", auth.AuthModeOAuth, ""),
		"gpt-5", logger, 1, 0,
	)
	list, err := client.ListModels(context.Background())
	assert.NoError(t, err)
	assert.Nil(t, list)
}

// Regression: the Responses API ships `input_tokens`/`output_tokens`,
// not `prompt_tokens`/`completion_tokens`. Before the parser split, this
// client called the Chat Completions parser on a Responses payload and
// silently returned zeroed usage — the chat envelope then rendered the
// "no tokens" placeholder for GPT instead of the input/output arrows.
// This test pins that the Responses parser is the one in use and that
// LastUsage() surfaces the parsed counts (and cache-hit count) verbatim.
func TestOpenAIResponsesClient_SendPrompt_SurfacesUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{
			"output_text": "ok",
			"status": "completed",
			"usage": {
				"input_tokens": 75,
				"input_tokens_details": {"cached_tokens": 32},
				"output_tokens": 1186,
				"output_tokens_details": {"reasoning_tokens": 1024},
				"total_tokens": 1261
			}
		}`)
	}))
	defer server.Close()

	require.NoError(t, os.Setenv("OPENAI_RESPONSES_API_URL", server.URL))
	defer os.Unsetenv("OPENAI_RESPONSES_API_URL")

	logger, _ := zap.NewDevelopment()
	client := NewOpenAIResponsesClient(testProvider("test-api-key"), "gpt-5", logger, 1, 0)
	_, err := client.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 0)
	require.NoError(t, err)

	usage := client.LastUsage()
	require.NotNil(t, usage, "Responses usage must reach LastUsage so the chat envelope renders arrows for GPT")
	assert.Equal(t, 75, usage.PromptTokens)
	assert.Equal(t, 1186, usage.CompletionTokens)
	assert.Equal(t, 1261, usage.TotalTokens)
	assert.Equal(t, 32, usage.CacheReadInputTokens)
	assert.Equal(t, 1024, usage.ReasoningTokens)
	assert.Equal(t, "completed", client.LastStopReason())
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
