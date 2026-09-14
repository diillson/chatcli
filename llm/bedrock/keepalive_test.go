package bedrock

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The Mantle surface refreshes by re-sending the remembered Messages body
// with one output token; the other families report unsupported.
func TestKeepPromptCacheWarmOnMantle(t *testing.T) {
	var bodies []map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var body map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &body))
		bodies = append(bodies, body)
		w.Header().Set("content-type", "application/json")
		if len(bodies) == 1 {
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"pong"}],"usage":{"input_tokens":5,"output_tokens":1,"cache_creation_input_tokens":3000}}`))
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"."}],"usage":{"input_tokens":0,"output_tokens":1,"cache_read_input_tokens":3005}}`))
	}))
	defer srv.Close()
	setEnvForTest(t, "BEDROCK_MANTLE_BASE_URL", srv.URL)
	setEnvForTest(t, "AWS_BEARER_TOKEN_BEDROCK", "test-bearer-token")

	c := &BedrockClient{model: "anthropic.claude-sonnet-5", region: "us-east-1", logger: zap.NewNop(), maxAttempts: 1}
	if _, err := c.KeepPromptCacheWarm(t.Context()); !errors.Is(err, client.ErrPromptCacheKeepAliveUnsupported) {
		t.Fatalf("before any request: got %v", err)
	}
	_, err := c.sendPromptAnthropicMantle(t.Context(), "ping", []models.Message{{Role: "user", Content: "ping"}}, 512)
	require.NoError(t, err)
	usage, err := c.KeepPromptCacheWarm(t.Context())
	require.NoError(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 3005, usage.CacheReadInputTokens)
	require.Len(t, bodies, 2)
	require.Equal(t, float64(1), bodies[1]["max_tokens"], "the refresh asks for one token")
	require.Equal(t, bodies[0]["messages"], bodies[1]["messages"], "same prefix, markers included")

	converse := &BedrockClient{model: "amazon.nova-2", region: "us-east-1", logger: zap.NewNop()}
	if _, err := converse.KeepPromptCacheWarm(t.Context()); !errors.Is(err, client.ErrPromptCacheKeepAliveUnsupported) {
		t.Fatalf("a non-Mantle family reports unsupported, got %v", err)
	}
}
