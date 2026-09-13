package claudeai

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

// A keep-alive re-sends the last request asking for no output, keeps
// thinking and effort (part of the cache key), drops the task budget
// (its beta header is bound to the turn's context) and returns the
// usage the provider reported.
func TestKeepPromptCacheWarmResendsTheLastPrefix(t *testing.T) {
	var bodies []map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":5,"output_tokens":1,"cache_creation_input_tokens":4000}}`))
			return
		}
		_, _ = w.Write([]byte(`{"content":[],"stop_reason":"max_tokens","usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":4005}}`))
	}))
	defer server.Close()

	c := NewClaudeClient(auth.NewStaticTokenProvider("sk-test", auth.AuthModeAPIKey, auth.ProviderAnthropic),
		"claude-fable-5-1", zap.NewNop(), 1, 0)
	c.apiURL = server.URL

	if _, err := c.KeepPromptCacheWarm(context.Background()); !errors.Is(err, client.ErrPromptCacheKeepAliveUnsupported) {
		t.Fatalf("before any request the client has nothing to keep warm, got %v", err)
	}
	ctx := client.WithEffortHint(context.Background(), client.EffortHigh)
	ctx = client.WithTaskBudget(ctx, client.AnthropicTaskBudget(50000))
	if _, err := c.SendPrompt(ctx, "hello", []models.Message{{Role: "system", Content: "sys"}}, 100); err != nil {
		t.Fatal(err)
	}
	usage, err := c.KeepPromptCacheWarm(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if usage == nil || usage.CacheReadInputTokens != 4005 || usage.CompletionTokens != 0 {
		t.Fatalf("keep-alive usage = %+v", usage)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected the turn and one refresh, got %d requests", len(bodies))
	}
	turn, refresh := bodies[0], bodies[1]
	if refresh["max_tokens"] != float64(0) {
		t.Errorf("refresh must ask for no output: %v", refresh["max_tokens"])
	}
	if _, ok := refresh["stream"]; ok {
		t.Error("refresh must not stream")
	}
	if turn["thinking"] == nil || refresh["thinking"] == nil {
		t.Errorf("thinking is part of the cache key and must travel on both: turn=%v refresh=%v", turn["thinking"], refresh["thinking"])
	}
	if cfg, _ := refresh["output_config"].(map[string]interface{}); cfg["task_budget"] != nil {
		t.Errorf("the task budget must not travel on a refresh: %v", cfg)
	}
	if cfg, _ := refresh["output_config"].(map[string]interface{}); cfg["effort"] != turn["output_config"].(map[string]interface{})["effort"] {
		t.Errorf("effort must be kept: %v vs %v", refresh["output_config"], turn["output_config"])
	}
	if b, _ := json.Marshal(turn["messages"]); string(b) != mustJSON(refresh["messages"]) {
		t.Error("the refresh must carry the same messages, markers included")
	}
}

func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// OAuth never carries cache markers, so there is nothing to keep warm.
func TestKeepPromptCacheWarmUnsupportedOnOAuth(t *testing.T) {
	c := NewClaudeClient(auth.NewStaticTokenProvider("tok", auth.AuthModeOAuth, auth.ProviderAnthropic),
		"claude-fable-5-1", zap.NewNop(), 1, 0)
	c.rememberRequest([]byte(`{"model":"x"}`))
	if _, err := c.KeepPromptCacheWarm(context.Background()); !errors.Is(err, client.ErrPromptCacheKeepAliveUnsupported) {
		t.Fatalf("got %v", err)
	}
	var none *ClaudeClient
	if _, err := none.KeepPromptCacheWarm(context.Background()); !errors.Is(err, client.ErrPromptCacheKeepAliveUnsupported) {
		t.Fatalf("nil client: got %v", err)
	}
}
