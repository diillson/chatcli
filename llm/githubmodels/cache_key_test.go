package githubmodels

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// The automatic-caching routing hint steers requests that share a prefix
// to the same shard. GitHub Models speaks the OpenAI chat schema, so it
// takes the same field as the rest of that family.
func TestGitHubModelsSendsPromptCacheKey(t *testing.T) {
	var body map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	t.Setenv("GITHUB_MODELS_API_URL", server.URL)
	c := NewGitHubModelsClient(testProvider("ghp-test"), "gpt-5.6-terra", zap.NewNop(), 1, 0)

	history := []models.Message{
		{Role: "system", Content: "You are a careful assistant."},
		{Role: "user", Content: "Hi"},
	}
	if _, err := c.SendPrompt(context.Background(), "Hi", history, 100); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	key, ok := body["prompt_cache_key"].(string)
	if !ok || key == "" {
		t.Fatalf("prompt_cache_key missing from the payload: %+v", body)
	}

	// The key is derived from the stable prefix, so a second turn of the
	// same session lands on the same shard.
	first := key
	body = nil
	history = append(history, models.Message{Role: "assistant", Content: "Hello"})
	if _, err := c.SendPrompt(context.Background(), "again", history, 100); err != nil {
		t.Fatalf("second SendPrompt: %v", err)
	}
	if body["prompt_cache_key"] != first {
		t.Errorf("key changed between turns: %v vs %v", body["prompt_cache_key"], first)
	}
}

// A conversation with no system message has no stable prefix to key on,
// so the field is omitted rather than sent empty.
func TestGitHubModelsOmitsCacheKeyWithoutASystemPrefix(t *testing.T) {
	var body map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	t.Setenv("GITHUB_MODELS_API_URL", server.URL)
	c := NewGitHubModelsClient(testProvider("ghp-test"), "gpt-4o", zap.NewNop(), 1, 0)
	if _, err := c.SendPrompt(context.Background(), "Hi", []models.Message{{Role: "user", Content: "Hi"}}, 100); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if _, ok := body["prompt_cache_key"]; ok {
		t.Errorf("no stable prefix means no key: %+v", body)
	}
}
