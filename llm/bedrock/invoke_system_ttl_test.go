/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestInvokeModel_SystemMarkerCarriesTheSameTTLAsHistory(t *testing.T) {
	t.Setenv(client.PromptCacheTTLEnv, "1h")
	history := []models.Message{
		{Role: "system", SystemParts: []models.ContentBlock{{Type: "text", Text: "stable charter", CacheControl: &models.CacheControl{Type: "ephemeral"}}, {Type: "text", Text: "volatile"}}},
		{Role: "user", Content: "q1"}, {Role: "assistant", Content: "a1"},
	}
	c := &BedrockClient{model: "anthropic.claude-sonnet-5", logger: zap.NewNop()}
	_, sys := c.buildMessagesAndSystem("q2", history)
	blocks, ok := sys.([]map[string]interface{})
	if !ok || len(blocks) == 0 {
		t.Fatalf("system blocks: %#v", sys)
	}
	cc, _ := blocks[0]["cache_control"].(map[string]string)
	if cc["ttl"] != "1h" {
		t.Fatalf("system marker must carry the 1h ttl like the history breakpoint (ordering rule): %#v", cc)
	}
	// Older Claude: no ttl anywhere.
	old := &BedrockClient{model: "anthropic.claude-3-7-sonnet-20250219-v1:0", logger: zap.NewNop()}
	_, sys = old.buildMessagesAndSystem("q2", history)
	blocks, _ = sys.([]map[string]interface{})
	cc, _ = blocks[0]["cache_control"].(map[string]string)
	if _, has := cc["ttl"]; has || cc["type"] != "ephemeral" {
		t.Fatalf("older Claude keeps the 5m default: %#v", cc)
	}
}
