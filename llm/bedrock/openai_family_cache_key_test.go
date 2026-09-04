package bedrock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// The OpenAI models served through Bedrock speak the same chat schema as
// the first-party endpoint, so they take the same automatic-caching shard
// hint as the rest of that family.
func TestSendPromptOpenAI_SendsPromptCacheKey(t *testing.T) {
	var body map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	c := &BedrockClient{
		model:       "openai.gpt-oss-120b-1:0",
		region:      "us-east-1",
		logger:      zap.NewNop(),
		runtime:     newFakeEndpointRuntime(srv.URL),
		maxAttempts: 1,
		backoff:     time.Millisecond,
	}

	history := []models.Message{
		{Role: "system", Content: "You are a careful assistant."},
		{Role: "user", Content: "ping"},
	}
	if _, err := c.sendPromptOpenAI(t.Context(), "ping", history, 256); err != nil {
		t.Fatalf("sendPromptOpenAI: %v", err)
	}
	if key, ok := body["prompt_cache_key"].(string); !ok || key == "" {
		t.Fatalf("prompt_cache_key missing from the payload: %+v", body)
	}
}

// No system message means no stable prefix to key on, so the field is
// omitted rather than sent empty.
func TestSendPromptOpenAI_OmitsCacheKeyWithoutASystemPrefix(t *testing.T) {
	var body map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	c := &BedrockClient{
		model:       "openai.gpt-oss-120b-1:0",
		region:      "us-east-1",
		logger:      zap.NewNop(),
		runtime:     newFakeEndpointRuntime(srv.URL),
		maxAttempts: 1,
		backoff:     time.Millisecond,
	}
	if _, err := c.sendPromptOpenAI(t.Context(), "ping", []models.Message{{Role: "user", Content: "ping"}}, 256); err != nil {
		t.Fatalf("sendPromptOpenAI: %v", err)
	}
	if _, ok := body["prompt_cache_key"]; ok {
		t.Errorf("no stable prefix means no key: %+v", body)
	}
}
