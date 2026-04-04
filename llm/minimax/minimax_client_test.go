package minimax

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

func newTestClient(url string) *MiniMaxClient {
	logger, _ := zap.NewDevelopment()
	c := NewMiniMaxClient("test-minimax-key", "MiniMax-M2.7", logger, 1, 0)
	c.apiURL = url
	return c
}

func TestMiniMaxClient_SendPrompt_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-minimax-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var payload map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&payload)
		require.NoError(t, err)
		assert.Equal(t, "MiniMax-M2.7", payload["model"])

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": "Hello from MiniMax!"}, "finish_reason": "stop"}]}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	history := []models.Message{{Role: "user", Content: "Hi"}}
	resp, err := c.SendPrompt(context.Background(), "Hi", history, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Hello from MiniMax!", resp)
}

func TestMiniMaxClient_SendPrompt_RetryOnTemporaryError(t *testing.T) {
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
	c := NewMiniMaxClient("test-minimax-key", "MiniMax-M2.7", logger, 2, 10*time.Millisecond)
	c.apiURL = server.URL

	resp, err := c.SendPrompt(context.Background(), "Test", []models.Message{{Role: "user", Content: "Test"}}, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Success on retry", resp)
	assert.Equal(t, 2, attempt, "Should have made two attempts")
}

func TestMiniMaxClient_SendPrompt_APIErrorInPayload(t *testing.T) {
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

func TestMiniMaxClient_SendPrompt_BaseRespError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"base_resp": {"status_code": 1001, "status_msg": "Insufficient balance"}, "choices": []}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	_, err := c.SendPrompt(context.Background(), "Test", []models.Message{{Role: "user", Content: "Test"}}, 0)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Insufficient balance")
	assert.Contains(t, err.Error(), "1001")
}

func TestMiniMaxClient_SendPrompt_EmptyChoices(t *testing.T) {
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

func TestMiniMaxClient_SendPrompt_EmptyContentWithLengthReason(t *testing.T) {
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

func TestMiniMaxClient_SendPrompt_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": {"message": "Invalid API key"}}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	_, err := c.SendPrompt(context.Background(), "Test", []models.Message{{Role: "user", Content: "Test"}}, 0)

	assert.Error(t, err)
}

func TestMiniMaxClient_SendPrompt_HistoryRolesNormalized(t *testing.T) {
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

func TestMiniMaxClient_SendPromptWithTools_Success(t *testing.T) {
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
						"id": "call_456",
						"type": "function",
						"function": {
							"name": "search_web",
							"arguments": "{\"query\": \"MiniMax AI\"}"
						}
					}]
				},
				"finish_reason": "tool_calls"
			}],
			"usage": {
				"prompt_tokens": 80,
				"completion_tokens": 30,
				"total_tokens": 110
			}
		}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	tools := []models.ToolDefinition{
		{
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "search_web",
				Description: "Search the web",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{"type": "string"},
					},
					"required": []string{"query"},
				},
			},
		},
	}

	resp, err := c.SendPromptWithTools(
		context.Background(),
		"Search for MiniMax AI",
		[]models.Message{{Role: "user", Content: "Search for MiniMax AI"}},
		tools,
		1000,
	)

	require.NoError(t, err)
	assert.Equal(t, "tool_calls", resp.StopReason)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_456", resp.ToolCalls[0].ID)
	assert.Equal(t, "search_web", resp.ToolCalls[0].Name)
	assert.Equal(t, "MiniMax AI", resp.ToolCalls[0].Arguments["query"])
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 80, resp.Usage.PromptTokens)
	assert.Equal(t, 30, resp.Usage.CompletionTokens)
	assert.Equal(t, 110, resp.Usage.TotalTokens)
}

func TestMiniMaxClient_SendPromptWithTools_TextResponse(t *testing.T) {
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

func TestMiniMaxClient_SendPromptWithTools_ToolResultHistory(t *testing.T) {
	var receivedPayload map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "The result is ready."}, "finish_reason": "stop"}]}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	history := []models.Message{
		{Role: "user", Content: "Search for something"},
		{
			Role:    "assistant",
			Content: "",
			ToolCalls: []models.ToolCall{
				{ID: "call_1", Name: "search_web", Type: "function", Arguments: map[string]interface{}{"query": "test"}},
			},
		},
		{Role: "tool", ToolCallID: "call_1", Content: `{"results": ["result1", "result2"]}`},
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

func TestMiniMaxClient_SendPromptWithTools_MultipleToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"tool_calls": [
						{
							"id": "call_a",
							"type": "function",
							"function": {"name": "read_file", "arguments": "{\"path\": \"/tmp/a.txt\"}"}
						},
						{
							"id": "call_b",
							"type": "function",
							"function": {"name": "read_file", "arguments": "{\"path\": \"/tmp/b.txt\"}"}
						}
					]
				},
				"finish_reason": "tool_calls"
			}]
		}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	resp, err := c.SendPromptWithTools(
		context.Background(),
		"Read both files",
		[]models.Message{{Role: "user", Content: "Read both files"}},
		nil,
		1000,
	)

	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 2)
	assert.Equal(t, "call_a", resp.ToolCalls[0].ID)
	assert.Equal(t, "call_b", resp.ToolCalls[1].ID)
	assert.Equal(t, "/tmp/a.txt", resp.ToolCalls[0].Arguments["path"])
	assert.Equal(t, "/tmp/b.txt", resp.ToolCalls[1].Arguments["path"])
}

func TestMiniMaxClient_ListModels_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "Bearer test-minimax-key", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "MiniMax-M2.7", "owned_by": "minimax"},
				{"id": "MiniMax-M2.5", "owned_by": "minimax"},
				{"id": "abab6.5s-chat", "owned_by": "minimax"},
				{"id": "speech-2.8-hd", "owned_by": "minimax"}
			]
		}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL + "/chat/completions")
	modelsList, err := c.ListModels(context.Background())

	require.NoError(t, err)
	// speech-2.8-hd should be filtered out (not minimax/abab prefix)
	assert.Len(t, modelsList, 3)
}

func TestMiniMaxClient_GetModelName(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	c := NewMiniMaxClient("key", "MiniMax-M2.7", logger, 1, 0)
	name := c.GetModelName()
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

	assert.Equal(t, "system", msgs[0].(map[string]interface{})["role"])
	assert.Equal(t, "user", msgs[1].(map[string]interface{})["role"])
	assert.Equal(t, "assistant", msgs[2].(map[string]interface{})["role"])
	assert.Equal(t, "Hi there!", msgs[2].(map[string]interface{})["content"])
	assert.Equal(t, "user", msgs[3].(map[string]interface{})["role"])

	assistantTC := msgs[4].(map[string]interface{})
	assert.Equal(t, "assistant", assistantTC["role"])
	assert.NotNil(t, assistantTC["tool_calls"])

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
					"id": "call_xyz",
					"type": "function",
					"function": {
						"name": "write_file",
						"arguments": "{\"path\": \"/tmp/out.txt\", \"content\": \"hello\"}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {
			"prompt_tokens": 200,
			"completion_tokens": 80,
			"total_tokens": 280
		}
	}`

	logger, _ := zap.NewDevelopment()
	resp, err := parseToolResponse(body, logger)

	require.NoError(t, err)
	assert.Equal(t, "Done!", resp.Content)
	assert.Equal(t, "tool_calls", resp.StopReason)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_xyz", resp.ToolCalls[0].ID)
	assert.Equal(t, "write_file", resp.ToolCalls[0].Name)
	assert.Equal(t, "/tmp/out.txt", resp.ToolCalls[0].Arguments["path"])
	assert.Equal(t, "hello", resp.ToolCalls[0].Arguments["content"])
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 200, resp.Usage.PromptTokens)
	assert.Equal(t, 80, resp.Usage.CompletionTokens)
	assert.Equal(t, 280, resp.Usage.TotalTokens)
}

func TestMiniMaxClient_ModelCaseSensitive(t *testing.T) {
	var receivedPayload map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "ok"}}]}`))
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	// MiniMax model IDs are case-sensitive: "MiniMax-M2.7" not "minimax-m2.7"
	c := NewMiniMaxClient("key", "MiniMax-M2.7", logger, 1, 0)
	c.apiURL = server.URL

	_, err := c.SendPrompt(context.Background(), "test", []models.Message{{Role: "user", Content: "test"}}, 100)
	require.NoError(t, err)

	assert.Equal(t, "MiniMax-M2.7", receivedPayload["model"])
}
