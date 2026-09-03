/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diillson/chatcli/auth"
	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// captureAnthropicRequest runs one SendPrompt against a stub server and
// returns the decoded request body plus the headers it carried.
func captureAnthropicRequest(t *testing.T, history []models.Message, prompt string) (map[string]interface{}, http.Header) {
	t.Helper()
	var body map[string]interface{}
	var headers http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		headers = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	c := NewClaudeClient(
		auth.NewStaticTokenProvider("sk-test", auth.AuthModeAPIKey, auth.ProviderAnthropic),
		"claude-sonnet-5", zap.NewNop(), 1, 0,
	)
	c.apiURL = server.URL
	if _, err := c.SendPrompt(context.Background(), prompt, history, 100); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	return body, headers
}

func lastMessageBlocks(t *testing.T, body map[string]interface{}) []interface{} {
	t.Helper()
	msgs, _ := body["messages"].([]interface{})
	if len(msgs) == 0 {
		t.Fatalf("no messages in body: %v", body)
	}
	last, _ := msgs[len(msgs)-1].(map[string]interface{})
	blocks, ok := last["content"].([]interface{})
	if !ok {
		t.Fatalf("last message content is not a block list: %#v", last["content"])
	}
	return blocks
}

func TestSendPrompt_HistoryBreakpointOnLastUserMessage(t *testing.T) {
	t.Setenv(llmclient.PromptCacheTTLEnv, "")
	history := []models.Message{
		{Role: "system", SystemParts: []models.ContentBlock{{Type: "text", Text: "sys", CacheControl: &models.CacheControl{Type: "ephemeral"}}}},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "second"},
	}
	body, headers := captureAnthropicRequest(t, history, "second")
	blocks := lastMessageBlocks(t, body)
	blk := blocks[len(blocks)-1].(map[string]interface{})
	cc, ok := blk["cache_control"].(map[string]interface{})
	if !ok || cc["type"] != "ephemeral" {
		t.Fatalf("last user block must carry cache_control, got %#v", blk)
	}
	if _, hasTTL := cc["ttl"]; hasTTL {
		t.Fatalf("default TTL must not send a ttl field: %#v", cc)
	}
	if strings.Contains(headers.Get("anthropic-beta"), llmclient.ExtendedCacheTTLBeta) {
		t.Fatal("no extended-TTL beta header without the 1h setting")
	}
	if n := llmclient.CountAnthropicCacheMarkers(body); n < 2 || n > anthropicMaxCacheBreakpoints {
		t.Fatalf("expected system + history markers within the cap, got %d", n)
	}
	// Earlier turns stay plain strings — only the tail is marked.
	msgs := body["messages"].([]interface{})
	if _, isStr := msgs[0].(map[string]interface{})["content"].(string); !isStr {
		t.Fatalf("first user message must stay a plain string: %#v", msgs[0])
	}
}

func TestSendPrompt_OneHourTTLMarkerAndBeta(t *testing.T) {
	t.Setenv(llmclient.PromptCacheTTLEnv, "1h")
	history := []models.Message{
		{Role: "system", SystemParts: []models.ContentBlock{{Type: "text", Text: "sys", CacheControl: &models.CacheControl{Type: "ephemeral"}}}},
		{Role: "user", Content: "hi"},
	}
	body, headers := captureAnthropicRequest(t, history, "hi")
	blk := lastMessageBlocks(t, body)[0].(map[string]interface{})
	cc, _ := blk["cache_control"].(map[string]interface{})
	if cc["ttl"] != "1h" {
		t.Fatalf("1h TTL must be on the marker, got %#v", cc)
	}
	sys := body["system"].([]interface{})[0].(map[string]interface{})
	if sysCC, _ := sys["cache_control"].(map[string]interface{}); sysCC["ttl"] != "1h" {
		t.Fatalf("system marker must carry the same TTL, got %#v", sys)
	}
	if !strings.Contains(headers.Get("anthropic-beta"), llmclient.ExtendedCacheTTLBeta) {
		t.Fatalf("extended-TTL beta header missing: %q", headers.Get("anthropic-beta"))
	}
}
