/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestPromptCacheKey_StablePerSystemPrompt(t *testing.T) {
	h1 := []models.Message{{Role: "system", Content: "You are ChatCLI."}, {Role: "user", Content: "a"}}
	h2 := []models.Message{{Role: "system", Content: "You are ChatCLI."}, {Role: "user", Content: "b"}, {Role: "assistant", Content: "c"}}
	k1, k2 := PromptCacheKey(h1), PromptCacheKey(h2)
	if k1 == "" || k1 != k2 {
		t.Fatalf("same system prompt must yield the same key across turns: %q vs %q", k1, k2)
	}
	if k3 := PromptCacheKey([]models.Message{{Role: "system", Content: "Other prompt"}}); k3 == k1 {
		t.Fatal("different system prompts must not collide")
	}
	if PromptCacheKey([]models.Message{{Role: "user", Content: "no system"}}) != "" {
		t.Fatal("no system message → no key")
	}
	parts := []models.Message{{Role: "system", SystemParts: []models.ContentBlock{{Type: "text", Text: "You are "}, {Type: "text", Text: "ChatCLI."}}}}
	if PromptCacheKey(parts) != k1 {
		t.Fatal("SystemParts must hash the same as their joined text")
	}
}

func TestParseAnthropicUsage_ExtendedTTLShare(t *testing.T) {
	result := map[string]interface{}{"usage": map[string]interface{}{
		"input_tokens":                float64(100),
		"output_tokens":               float64(20),
		"cache_creation_input_tokens": float64(5000),
		"cache_read_input_tokens":     float64(9000),
		"cache_creation": map[string]interface{}{
			"ephemeral_5m_input_tokens": float64(1000),
			"ephemeral_1h_input_tokens": float64(4000),
		},
	}}
	info := ParseAnthropicUsage(result)
	if info == nil || info.CacheCreationInputTokens != 5000 || info.CacheCreation1hInputTokens != 4000 || info.CacheReadInputTokens != 9000 {
		t.Fatalf("usage = %+v", info)
	}
}
