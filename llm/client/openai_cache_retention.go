/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import "strings"

// ExtendedPromptCache reports whether the operator asked for the long
// cache lifetime (CHATCLI_PROMPT_CACHE_TTL=1h, or auto resolved to 1h).
// One preference drives every provider that offers a longer retention:
// Anthropic's 1h ttl and OpenAI's 24h prompt_cache_retention.
func ExtendedPromptCache() bool { return AnthropicCacheTTL() == "1h" }

// OpenAIPromptCacheRetention returns the prompt_cache_retention value to
// send for model, "" when the field must not be sent. The field is
// documented for gpt-5.5 / gpt-5.5-pro ("24h" is the only value they
// accept) and deprecated from gpt-5.6 on in favor of
// prompt_cache_options, whose ttl currently has a single value (30m) —
// so nothing is sent there. Older models are left alone: an unknown
// field is a 400 on strict upstreams.
func OpenAIPromptCacheRetention(model string) string {
	if !ExtendedPromptCache() {
		return ""
	}
	m := strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(m, "gpt-5.5") {
		return "24h"
	}
	return ""
}
