package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// A refresh re-sends the last Chat Completions request asking for one
// token and returns the usage, cached share included.
func TestKeepPromptCacheWarmResendsTheLastPrefix(t *testing.T) {
	var bodies []map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3000,"completion_tokens":2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"."},"finish_reason":"length"}],"usage":{"prompt_tokens":3001,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":2944}}}`))
	}))
	defer server.Close()
	t.Setenv("OPENAI_API_URL", server.URL)

	c := NewOpenAIClient(auth.NewStaticTokenProvider("sk-test", auth.AuthModeAPIKey, auth.ProviderOpenAI), "gpt-5.6", zap.NewNop(), 1, 0)
	if _, err := c.KeepPromptCacheWarm(context.Background()); !errors.Is(err, client.ErrPromptCacheKeepAliveUnsupported) {
		t.Fatalf("before any request: got %v", err)
	}
	if _, err := c.SendPrompt(context.Background(), "hello", []models.Message{{Role: "user", Content: "hello"}}, 100); err != nil {
		t.Fatal(err)
	}
	usage, err := c.KeepPromptCacheWarm(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if usage == nil || usage.CacheReadInputTokens != 2944 || usage.CompletionTokens != 1 {
		t.Fatalf("keep-alive usage = %+v", usage)
	}
	if len(bodies) != 2 || bodies[1]["max_tokens"] != float64(1) {
		t.Fatalf("the refresh must ask for one token: %v", bodies)
	}
	if bodies[1]["prompt_cache_key"] != bodies[0]["prompt_cache_key"] {
		t.Error("the refresh must land on the same cache shard")
	}
	var none *OpenAIClient
	if _, err := none.KeepPromptCacheWarm(context.Background()); !errors.Is(err, client.ErrPromptCacheKeepAliveUnsupported) {
		t.Fatalf("nil client: got %v", err)
	}
}
