package zai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestClient(url string) *ZAIClient {
	logger, _ := zap.NewDevelopment()
	c := NewZAIClient("test-zai-key", "glm-4.7", logger, 1, 0)
	c.apiURL = url
	return c
}

func TestZAIClient_SendPrompt_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-zai-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var payload map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&payload)
		require.NoError(t, err)
		assert.Equal(t, "glm-4.7", payload["model"])

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": "Hello from ZAI!"}, "finish_reason": "stop"}]}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	history := []models.Message{{Role: "user", Content: "Hi"}}
	resp, err := c.SendPrompt(context.Background(), "Hi", history, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Hello from ZAI!", resp)
}

func TestZAIClient_SendPrompt_RetryOnTemporaryError(t *testing.T) {
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
	c := NewZAIClient("test-zai-key", "glm-4.7", logger, 2, 10*time.Millisecond)
	c.apiURL = server.URL

	resp, err := c.SendPrompt(context.Background(), "Test", []models.Message{{Role: "user", Content: "Test"}}, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Success on retry", resp)
	assert.Equal(t, 2, attempt, "Should have made two attempts")
}

func TestZAIClient_SendPrompt_APIErrorInPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error": {"message": "Invalid model specified"}, "choices": []}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	_, err := c.SendPrompt(context.Background(), "Test", []models.Message{{Role: "user", Content: "Test"}}, 0)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid model specified")
}

func TestZAIClient_SendPrompt_EmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": []}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	_, err := c.SendPrompt(context.Background(), "Test", []models.Message{{Role: "user", Content: "Test"}}, 0)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "choices")
}

func TestZAIClient_SendPrompt_EmptyContentWithLengthReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": ""}, "finish_reason": "length"}]}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	_, err := c.SendPrompt(context.Background(), "Test", []models.Message{{Role: "user", Content: "Test"}}, 0)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "max_tokens")
}

func TestZAIClient_SendPrompt_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": {"message": "Invalid API key"}}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	_, err := c.SendPrompt(context.Background(), "Test", []models.Message{{Role: "user", Content: "Test"}}, 0)

	assert.Error(t, err)
}

func TestZAIClient_SendPrompt_HistoryRolesNormalized(t *testing.T) {
	var receivedPayload map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "ok"}}]}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	history := []models.Message{
		{Role: "system", Content: "You are helpful."},
		{Role: "USER", Content: "Hello"},
		{Role: "ASSISTANT", Content: "Hi"},
		{Role: "unknown_role", Content: "Extra"},
	}

	_, err := c.SendPrompt(context.Background(), "Next", history, 100)
	require.NoError(t, err)

	msgs := receivedPayload["messages"].([]interface{})
	assert.Equal(t, "system", msgs[0].(map[string]interface{})["role"])
	assert.Equal(t, "user", msgs[1].(map[string]interface{})["role"])
	assert.Equal(t, "assistant", msgs[2].(map[string]interface{})["role"])
	assert.Equal(t, "user", msgs[3].(map[string]interface{})["role"]) // unknown -> user
}

func TestZAIClient_SendPromptWithTools_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)

		// Verify tools are sent
		tools, ok := payload["tools"].([]interface{})
		require.True(t, ok, "tools should be present in payload")
		assert.Len(t, tools, 1)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"content": null,
					"tool_calls": [{
						"id": "call_123",
						"type": "function",
						"function": {
							"name": "get_weather",
							"arguments": "{\"city\": \"Beijing\"}"
						}
					}]
				},
				"finish_reason": "tool_calls"
			}],
			"usage": {
				"prompt_tokens": 50,
				"completion_tokens": 20,
				"total_tokens": 70
			}
		}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	tools := []models.ToolDefinition{
		{
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "get_weather",
				Description: "Get weather for a city",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"city": map[string]interface{}{"type": "string"},
					},
					"required": []string{"city"},
				},
			},
		},
	}

	resp, err := c.SendPromptWithTools(
		context.Background(),
		"What's the weather in Beijing?",
		[]models.Message{{Role: "user", Content: "What's the weather in Beijing?"}},
		tools,
		1000,
	)

	require.NoError(t, err)
	assert.Equal(t, "tool_calls", resp.StopReason)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_123", resp.ToolCalls[0].ID)
	assert.Equal(t, "get_weather", resp.ToolCalls[0].Name)
	assert.Equal(t, "Beijing", resp.ToolCalls[0].Arguments["city"])
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 50, resp.Usage.PromptTokens)
	assert.Equal(t, 20, resp.Usage.CompletionTokens)
	assert.Equal(t, 70, resp.Usage.TotalTokens)
}

func TestZAIClient_SendPromptWithTools_TextResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"content": "I can help with that!"
				},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
		}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	resp, err := c.SendPromptWithTools(
		context.Background(),
		"Hello",
		[]models.Message{{Role: "user", Content: "Hello"}},
		nil,
		1000,
	)

	require.NoError(t, err)
	assert.Equal(t, "I can help with that!", resp.Content)
	assert.Equal(t, "stop", resp.StopReason)
	assert.Empty(t, resp.ToolCalls)
}

func TestZAIClient_SendPromptWithTools_ToolResultHistory(t *testing.T) {
	var receivedPayload map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "The weather is sunny."}, "finish_reason": "stop"}]}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	history := []models.Message{
		{Role: "user", Content: "What's the weather?"},
		{
			Role:    "assistant",
			Content: "",
			ToolCalls: []models.ToolCall{
				{ID: "call_1", Name: "get_weather", Type: "function", Arguments: map[string]interface{}{"city": "Beijing"}},
			},
		},
		{Role: "tool", ToolCallID: "call_1", Content: `{"temp": "25C", "condition": "sunny"}`},
	}

	_, err := c.SendPromptWithTools(context.Background(), "", history, nil, 1000)
	require.NoError(t, err)

	msgs := receivedPayload["messages"].([]interface{})
	require.Len(t, msgs, 3)

	// Verify tool result message
	toolMsg := msgs[2].(map[string]interface{})
	assert.Equal(t, "tool", toolMsg["role"])
	assert.Equal(t, "call_1", toolMsg["tool_call_id"])
}

func TestZAIClient_ListModels_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "Bearer test-zai-key", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "glm-5", "owned_by": "zhipu"},
				{"id": "glm-4.7", "owned_by": "zhipu"},
				{"id": "codegeex-4", "owned_by": "zhipu"},
				{"id": "embedding-3", "owned_by": "zhipu"}
			]
		}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL + "/chat/completions")
	modelsList, err := c.ListModels(context.Background())

	require.NoError(t, err)
	// embedding-3 should be filtered out (not glm-/codegeex/cogview/charglm prefix)
	assert.Len(t, modelsList, 3)
}

func TestZAIClient_GetModelName(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	c := NewZAIClient("key", "glm-4.7", logger, 1, 0)
	name := c.GetModelName()
	// Should return display name from catalog or the model id itself
	assert.NotEmpty(t, name)
}

func TestBuildToolMessages_WithAllRoles(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
		{Role: "user", Content: "Use a tool"},
		{
			Role: "assistant",
			ToolCalls: []models.ToolCall{
				{ID: "tc1", Name: "search", Type: "function", Arguments: map[string]interface{}{"q": "test"}},
			},
		},
		{Role: "tool", ToolCallID: "tc1", Content: "result data"},
	}

	msgs := buildToolMessages("", history)
	require.Len(t, msgs, 6)

	// system
	assert.Equal(t, "system", msgs[0].(map[string]interface{})["role"])
	// user
	assert.Equal(t, "user", msgs[1].(map[string]interface{})["role"])
	// assistant text
	assert.Equal(t, "assistant", msgs[2].(map[string]interface{})["role"])
	assert.Equal(t, "Hi there!", msgs[2].(map[string]interface{})["content"])
	// user
	assert.Equal(t, "user", msgs[3].(map[string]interface{})["role"])
	// assistant with tool_calls
	assistantTC := msgs[4].(map[string]interface{})
	assert.Equal(t, "assistant", assistantTC["role"])
	assert.NotNil(t, assistantTC["tool_calls"])
	// tool result
	toolMsg := msgs[5].(map[string]interface{})
	assert.Equal(t, "tool", toolMsg["role"])
	assert.Equal(t, "tc1", toolMsg["tool_call_id"])
}

func TestParseToolResponse_WithUsage(t *testing.T) {
	body := `{
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "Done!",
				"tool_calls": [{
					"id": "call_abc",
					"type": "function",
					"function": {
						"name": "read_file",
						"arguments": "{\"path\": \"/tmp/test.txt\"}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 50,
			"total_tokens": 150
		}
	}`

	logger, _ := zap.NewDevelopment()
	resp, err := parseToolResponse(body, logger)

	require.NoError(t, err)
	assert.Equal(t, "Done!", resp.Content)
	assert.Equal(t, "tool_calls", resp.StopReason)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_abc", resp.ToolCalls[0].ID)
	assert.Equal(t, "read_file", resp.ToolCalls[0].Name)
	assert.Equal(t, "/tmp/test.txt", resp.ToolCalls[0].Arguments["path"])
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 100, resp.Usage.PromptTokens)
	assert.Equal(t, 50, resp.Usage.CompletionTokens)
	assert.Equal(t, 150, resp.Usage.TotalTokens)
}
