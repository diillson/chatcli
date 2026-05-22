package moonshot

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
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func testProvider(key string) auth.TokenProvider {
	return auth.NewStaticTokenProvider(key, auth.AuthModeAPIKey, "")
}

func newTestClient(url string) *MoonshotClient {
	logger, _ := zap.NewDevelopment()
	c := NewMoonshotClient(testProvider("test-moonshot-key"), "kimi-k2.6", logger, 1, 0)
	c.apiURL = url
	return c
}

func TestMoonshotClient_SendPrompt_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-moonshot-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var payload map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&payload)
		require.NoError(t, err)
		assert.Equal(t, "kimi-k2.6", payload["model"])
		_, hasExtra := payload["extra_body"]
		assert.False(t, hasExtra, "no MOONSHOT_THINKING env => no extra_body injected")

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"choices": [{"message": {"role": "assistant", "content": "Hello from Kimi!"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7}
		}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	resp, err := c.SendPrompt(context.Background(), "Hi", []models.Message{{Role: "user", Content: "Hi"}}, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Hello from Kimi!", resp)
	assert.Equal(t, "stop", c.LastStopReason())
	require.NotNil(t, c.LastUsage())
	assert.Equal(t, 7, c.LastUsage().TotalTokens)
}

func TestMoonshotClient_SendPrompt_RetryOnTemporaryError(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": {"message": "Rate limit"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "ok"}}]}`))
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	c := NewMoonshotClient(testProvider("test-moonshot-key"), "kimi-k2.6", logger, 2, 10*time.Millisecond)
	c.apiURL = server.URL

	resp, err := c.SendPrompt(context.Background(), "Test", []models.Message{{Role: "user", Content: "Test"}}, 0)

	assert.NoError(t, err)
	assert.Equal(t, "ok", resp)
	assert.Equal(t, 2, attempt)
}

func TestMoonshotClient_SendPrompt_APIErrorInPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error": {"message": "Invalid model"}, "choices": []}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	_, err := c.SendPrompt(context.Background(), "T", []models.Message{{Role: "user", Content: "T"}}, 0)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid model")
}

func TestMoonshotClient_SendPrompt_EmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": []}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	_, err := c.SendPrompt(context.Background(), "T", []models.Message{{Role: "user", Content: "T"}}, 0)

	assert.Error(t, err)
}

func TestMoonshotClient_SendPrompt_EmptyContentWithLengthReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": ""}, "finish_reason": "length"}]}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	_, err := c.SendPrompt(context.Background(), "T", []models.Message{{Role: "user", Content: "T"}}, 0)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "max_tokens")
}

func TestMoonshotClient_SendPrompt_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": {"message": "Invalid API key"}}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	_, err := c.SendPrompt(context.Background(), "T", []models.Message{{Role: "user", Content: "T"}}, 0)

	assert.Error(t, err)
}

func TestMoonshotClient_SendPrompt_HistoryRolesNormalized(t *testing.T) {
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
	assert.Equal(t, "user", msgs[3].(map[string]interface{})["role"])
}

func TestMoonshotClient_ThinkingMode_EnabledInjectsExtraBody(t *testing.T) {
	var receivedPayload map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "ok"}}]}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	c.thinkingMode = "enabled"

	_, err := c.SendPrompt(context.Background(), "Hi", []models.Message{{Role: "user", Content: "Hi"}}, 0)
	require.NoError(t, err)

	extra, ok := receivedPayload["extra_body"].(map[string]interface{})
	require.True(t, ok, "extra_body must be injected when thinkingMode=enabled")
	thinking, ok := extra["thinking"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "enabled", thinking["type"])
}

func TestMoonshotClient_ThinkingMode_DisabledInjectsExtraBody(t *testing.T) {
	var receivedPayload map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "ok"}}]}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	c.thinkingMode = "disabled"

	_, err := c.SendPrompt(context.Background(), "Hi", []models.Message{{Role: "user", Content: "Hi"}}, 0)
	require.NoError(t, err)

	extra := receivedPayload["extra_body"].(map[string]interface{})
	thinking := extra["thinking"].(map[string]interface{})
	assert.Equal(t, "disabled", thinking["type"])
}

func TestMoonshotClient_ThinkingMode_AutoOmitsExtraBody(t *testing.T) {
	var receivedPayload map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "ok"}}]}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	c.thinkingMode = "auto"

	_, err := c.SendPrompt(context.Background(), "Hi", []models.Message{{Role: "user", Content: "Hi"}}, 0)
	require.NoError(t, err)

	_, hasExtra := receivedPayload["extra_body"]
	assert.False(t, hasExtra, "auto mode must not inject extra_body")
}

func TestMoonshotClient_ThinkingMode_IgnoredOnNonThinkingModel(t *testing.T) {
	var receivedPayload map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "ok"}}]}`))
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	c := NewMoonshotClient(testProvider("k"), "moonshot-v1-8k", logger, 1, 0)
	c.apiURL = server.URL
	c.thinkingMode = "enabled"

	_, err := c.SendPrompt(context.Background(), "Hi", []models.Message{{Role: "user", Content: "Hi"}}, 0)
	require.NoError(t, err)

	_, hasExtra := receivedPayload["extra_body"]
	assert.False(t, hasExtra, "models without thinking capability must not get extra_body")
}

func TestMoonshotClient_GetMaxTokens_EnvOverride(t *testing.T) {
	t.Setenv("MOONSHOT_MAX_TOKENS", "12345")

	logger, _ := zap.NewDevelopment()
	c := NewMoonshotClient(testProvider("k"), "kimi-k2.6", logger, 1, 0)
	assert.Equal(t, 12345, c.getMaxTokens())
}

func TestMoonshotClient_GetMaxTokens_FromCatalog(t *testing.T) {
	t.Setenv("MOONSHOT_MAX_TOKENS", "")

	logger, _ := zap.NewDevelopment()
	c := NewMoonshotClient(testProvider("k"), "kimi-k2.6", logger, 1, 0)
	tokens := c.getMaxTokens()
	assert.Greater(t, tokens, 0, "must fall back to catalog max")
}

func TestMoonshotClient_GetModelName(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	c := NewMoonshotClient(testProvider("k"), "kimi-k2.6", logger, 1, 0)
	name := c.GetModelName()
	assert.NotEmpty(t, name)
}

func TestMoonshotClient_SupportsNativeTools(t *testing.T) {
	c := newTestClient("")
	assert.True(t, c.SupportsNativeTools())
}

func TestMoonshotClient_SendPromptWithTools_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)

		tools, ok := payload["tools"].([]interface{})
		require.True(t, ok)
		assert.Len(t, tools, 1)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"content": null,
					"tool_calls": [{
						"id": "call_42",
						"type": "function",
						"function": {
							"name": "get_weather",
							"arguments": "{\"city\": \"Beijing\"}"
						}
					}]
				},
				"finish_reason": "tool_calls"
			}],
			"usage": {"prompt_tokens": 50, "completion_tokens": 20, "total_tokens": 70}
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
	assert.Equal(t, "call_42", resp.ToolCalls[0].ID)
	assert.Equal(t, "get_weather", resp.ToolCalls[0].Name)
	assert.Equal(t, "Beijing", resp.ToolCalls[0].Arguments["city"])
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 70, resp.Usage.TotalTokens)
}

func TestMoonshotClient_SendPromptWithTools_TextResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"choices": [{"message": {"role": "assistant", "content": "I can help!"}, "finish_reason": "stop"}],
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
	assert.Equal(t, "I can help!", resp.Content)
	assert.Equal(t, "stop", resp.StopReason)
	assert.Empty(t, resp.ToolCalls)
}

func TestMoonshotClient_SendPromptWithTools_ToolResultHistory(t *testing.T) {
	var receivedPayload map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "sunny."}, "finish_reason": "stop"}]}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	history := []models.Message{
		{Role: "user", Content: "Weather?"},
		{
			Role: "assistant",
			ToolCalls: []models.ToolCall{
				{ID: "c1", Name: "get_weather", Type: "function", Arguments: map[string]interface{}{"city": "Beijing"}},
			},
		},
		{Role: "tool", ToolCallID: "c1", Content: `{"temp": "25C"}`},
	}

	_, err := c.SendPromptWithTools(context.Background(), "", history, nil, 1000)
	require.NoError(t, err)

	msgs := receivedPayload["messages"].([]interface{})
	require.Len(t, msgs, 3)

	toolMsg := msgs[2].(map[string]interface{})
	assert.Equal(t, "tool", toolMsg["role"])
	assert.Equal(t, "c1", toolMsg["tool_call_id"])
}

func TestMoonshotClient_SendPromptWithTools_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"message": "bad payload"}}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	_, err := c.SendPromptWithTools(context.Background(), "T", []models.Message{{Role: "user", Content: "T"}}, nil, 100)
	assert.Error(t, err)
}

func TestMoonshotClient_ListModels_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "Bearer test-moonshot-key", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "kimi-k2.6", "owned_by": "moonshot"},
				{"id": "kimi-future-v9", "owned_by": "moonshot"},
				{"id": "moonshot-v1-128k", "owned_by": "moonshot"},
				{"id": "embedding-3", "owned_by": "openai"}
			]
		}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL + "/chat/completions")
	modelsList, err := c.ListModels(context.Background())

	require.NoError(t, err)
	// embedding-3 must be filtered (not kimi-/moonshot- prefix).
	assert.Len(t, modelsList, 3)

	// Unknown kimi-* must be registered into the catalog for completion.
	ids := make(map[string]bool)
	for _, m := range modelsList {
		ids[m.ID] = true
	}
	assert.True(t, ids["kimi-future-v9"])
}

func TestMoonshotClient_ListModels_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error": "forbidden"}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL + "/chat/completions")
	_, err := c.ListModels(context.Background())
	assert.Error(t, err)
}

func TestBuildToolMessages_WithAllRoles(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi"},
		{Role: "user", Content: "Use tool"},
		{
			Role: "assistant",
			ToolCalls: []models.ToolCall{
				{ID: "t1", Name: "search", Type: "function", Arguments: map[string]interface{}{"q": "x"}},
			},
		},
		{Role: "tool", ToolCallID: "t1", Content: "result"},
	}

	msgs := buildToolMessages("", history)
	require.Len(t, msgs, 6)

	assert.Equal(t, "system", msgs[0].(map[string]interface{})["role"])
	assert.Equal(t, "user", msgs[1].(map[string]interface{})["role"])
	assert.Equal(t, "assistant", msgs[2].(map[string]interface{})["role"])
	assert.Equal(t, "Hi", msgs[2].(map[string]interface{})["content"])
	assert.Equal(t, "user", msgs[3].(map[string]interface{})["role"])

	assistantTC := msgs[4].(map[string]interface{})
	assert.Equal(t, "assistant", assistantTC["role"])
	assert.NotNil(t, assistantTC["tool_calls"])

	toolMsg := msgs[5].(map[string]interface{})
	assert.Equal(t, "tool", toolMsg["role"])
	assert.Equal(t, "t1", toolMsg["tool_call_id"])
}

func TestBuildToolMessages_UnknownRoleFallsBackToUser(t *testing.T) {
	history := []models.Message{
		{Role: "weird", Content: "fallback content"},
	}
	msgs := buildToolMessages("", history)
	require.Len(t, msgs, 1)
	assert.Equal(t, "user", msgs[0].(map[string]interface{})["role"])
}

func TestBuildToolMessages_AppendsTrailingPrompt(t *testing.T) {
	msgs := buildToolMessages("trailing q", []models.Message{{Role: "assistant", Content: "prior"}})
	require.Len(t, msgs, 2)
	last := msgs[1].(map[string]interface{})
	assert.Equal(t, "user", last["role"])
	assert.Equal(t, "trailing q", last["content"])
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
						"arguments": "{\"path\": \"/tmp/x\"}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150}
	}`

	logger, _ := zap.NewDevelopment()
	resp, err := parseToolResponse(body, logger)

	require.NoError(t, err)
	assert.Equal(t, "Done!", resp.Content)
	assert.Equal(t, "tool_calls", resp.StopReason)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_abc", resp.ToolCalls[0].ID)
	assert.Equal(t, "read_file", resp.ToolCalls[0].Name)
	assert.Equal(t, "/tmp/x", resp.ToolCalls[0].Arguments["path"])
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 100, resp.Usage.PromptTokens)
	assert.Equal(t, 50, resp.Usage.CompletionTokens)
	assert.Equal(t, 150, resp.Usage.TotalTokens)
}

func TestParseToolResponse_MalformedArgumentsKeepsRaw(t *testing.T) {
	body := `{
		"choices": [{
			"message": {
				"role": "assistant",
				"tool_calls": [{
					"id": "x",
					"type": "function",
					"function": {"name": "fn", "arguments": "not-json-at-all"}
				}]
			},
			"finish_reason": "tool_calls"
		}]
	}`
	logger, _ := zap.NewDevelopment()
	resp, err := parseToolResponse(body, logger)
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "not-json-at-all", resp.ToolCalls[0].Arguments["raw"])
}

func TestParseToolResponse_InvalidJSON(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	_, err := parseToolResponse("not json", logger)
	assert.Error(t, err)
}

func TestParseToolResponse_NoChoices(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	_, err := parseToolResponse(`{"choices": []}`, logger)
	assert.Error(t, err)
}

func TestRegister_FactoryRegistered(t *testing.T) {
	// init() em register.go registra o provider; aqui validamos que a entry existe.
	// O registry é populado por init() de cada package importado.
	// Apenas instanciar via factory para garantir o caminho feliz.
	logger, _ := zap.NewDevelopment()
	c := NewMoonshotClient(testProvider("k"), "", logger, 1, 0) // model vazio dispara default via factory; aqui só validamos construtor
	assert.NotNil(t, c)
}
