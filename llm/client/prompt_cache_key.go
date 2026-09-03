/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/diillson/chatcli/models"
)

// PromptCacheKey derives the routing hint OpenAI's automatic prompt caching
// accepts as prompt_cache_key: requests sharing a key are steered to the
// same cache shard, which raises the hit rate on multi-instance backends.
// The key is the hash of the conversation's stable prefix (the first system
// message), so every turn of one session — and any session with the same
// system prompt — lands on the same shard, while the secret-free digest
// never reveals prompt content. Empty when the history carries no system
// message; callers then omit the field.
func PromptCacheKey(history []models.Message) string {
	for _, msg := range history {
		if !strings.EqualFold(strings.TrimSpace(msg.Role), "system") {
			continue
		}
		text := msg.Content
		if text == "" && len(msg.SystemParts) > 0 {
			var b strings.Builder
			for _, p := range msg.SystemParts {
				b.WriteString(p.Text)
			}
			text = b.String()
		}
		if strings.TrimSpace(text) == "" {
			return ""
		}
		sum := sha256.Sum256([]byte(text))
		return "chatcli-" + hex.EncodeToString(sum[:8])
	}
	return ""
}
