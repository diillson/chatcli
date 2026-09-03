/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Anthropic prompt-cache helpers shared by the claudeai and bedrock
 * adapters: the cache_control marker (with the optional 1-hour TTL) and the
 * rolling conversation breakpoint.
 *
 * Anthropic's cache is prefix-based and a request may carry at most four
 * cache_control markers. Until now ChatCLI marked only the system blocks and
 * the last tool definition, so the growing conversation — the bulk of every
 * agent turn — was re-tokenized at full price on each request. Marking the
 * last content block of the last message makes the whole conversation a
 * cacheable prefix: the next turn's request shares that prefix and the API
 * serves it from cache (it looks back across the previous breakpoint
 * positions automatically), then writes a new entry at the new tail.
 */
package client

import (
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
)

// ContextEngineEnv selects the context engine (cli reads builtin|mcp:<server>|
// provider); "provider" asks the model's own server-side context editing
// (Anthropic context management) to drop stale tool results before they
// are billed, in addition to ChatCLI's local compaction.
const ContextEngineEnv = "CHATCLI_CONTEXT_ENGINE"

// ContextManagementBeta is the Anthropic beta that enables context editing.
const ContextManagementBeta = "context-management-2025-06-27"

// ProviderContextEngine reports whether CHATCLI_CONTEXT_ENGINE=provider.
func ProviderContextEngine() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(ContextEngineEnv)), "provider")
}

// ContextThreshold is a typed amount in an Anthropic context edit
// ("input_tokens" or "tool_uses" with a value).
type ContextThreshold struct {
	Type  string `json:"type"`
	Value int    `json:"value"`
}

// ContextEdit is one Anthropic context-editing strategy.
type ContextEdit struct {
	Type         string            `json:"type"`
	Trigger      *ContextThreshold `json:"trigger,omitempty"`
	Keep         *ContextThreshold `json:"keep,omitempty"`
	ClearAtLeast *ContextThreshold `json:"clear_at_least,omitempty"`
}

// ContextManagement is the context_management request block.
type ContextManagement struct {
	Edits []ContextEdit `json:"edits"`
}

// AnthropicContextManagement returns the context_management request block
// for the provider context engine: clear the oldest tool results once the
// prompt passes 100K input tokens, keeping the five most recent tool uses
// and freeing at least 20K tokens per edit. Nil when the engine is off.
func AnthropicContextManagement() *ContextManagement {
	if !ProviderContextEngine() {
		return nil
	}
	return &ContextManagement{Edits: []ContextEdit{{
		Type:         "clear_tool_uses_20250919",
		Trigger:      &ContextThreshold{Type: "input_tokens", Value: 100000},
		Keep:         &ContextThreshold{Type: "tool_uses", Value: 5},
		ClearAtLeast: &ContextThreshold{Type: "input_tokens", Value: 20000},
	}}}
}

// PromptCacheTTLEnv selects the Anthropic cache lifetime: "5m" (default) or
// "1h". The hour keeps the prefix warm through longer idle gaps at a higher
// write rate (2x input instead of 1.25x), so it pays off for sessions that
// pause between turns and costs more for rapid-fire ones.
const PromptCacheTTLEnv = "CHATCLI_PROMPT_CACHE_TTL"

// ExtendedCacheTTLBeta is the anthropic-beta feature flag the 1-hour TTL
// still travels under on the Messages API.
const ExtendedCacheTTLBeta = "extended-cache-ttl-2025-04-11"

// promptCacheTTLHint is the lifetime the running surface asks for when the
// env says "auto": the coder/agent loop sets "1h" for its turns (long
// sessions that pause between tool rounds) and restores "5m" after; chat
// and one-shot never touch it. Read only when the env is "auto".
var promptCacheTTLHint atomic.Value // string

// SetPromptCacheTTLHint records the surface preference honored by
// CHATCLI_PROMPT_CACHE_TTL=auto ("5m" or "1h"; anything else resets to
// the 5-minute default).
func SetPromptCacheTTLHint(ttl string) {
	if ttl != "1h" {
		ttl = "5m"
	}
	promptCacheTTLHint.Store(ttl)
}

// AnthropicCacheTTL returns the configured cache lifetime, normalized to
// "5m" or "1h". "auto" defers to the surface hint (SetPromptCacheTTLHint);
// anything else resolves to the 5-minute default so a typo can never
// disable caching or send an invalid ttl.
func AnthropicCacheTTL() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(PromptCacheTTLEnv))) {
	case "1h", "60m", "hour":
		return "1h"
	case "auto":
		if v, _ := promptCacheTTLHint.Load().(string); v == "1h" {
			return "1h"
		}
		return "5m"
	default:
		return "5m"
	}
}

// AnthropicCacheMarker builds the cache_control value every marker in a
// request uses. One TTL for all markers: Anthropic requires longer-lived
// entries to precede shorter ones, and a uniform lifetime satisfies that
// trivially.
func AnthropicCacheMarker() map[string]string {
	m := map[string]string{"type": "ephemeral"}
	if AnthropicCacheTTL() == "1h" {
		m["ttl"] = "1h"
	}
	return m
}

// AnthropicCacheMarkerWithTTL is AnthropicCacheMarker for adapters whose
// wire does not accept the ttl field (Bedrock InvokeModel keeps the
// 5-minute default regardless of the env).
func AnthropicCacheMarkerWithTTL(allowExtended bool) map[string]string {
	if !allowExtended {
		return map[string]string{"type": "ephemeral"}
	}
	return AnthropicCacheMarker()
}

// CacheMarker is the cache_control value placed on a content block.
type CacheMarker map[string]string

// AnthropicMessages carries a request's message list in whichever slice
// shape the adapter built: the buffered clients produce a map slice, the
// tool-use builder a generic list. Exactly one field is set.
type AnthropicMessages struct {
	Maps []map[string]interface{}
	List []interface{}
}

// last returns the final message of whichever list is populated.
func (m AnthropicMessages) last() map[string]interface{} {
	if n := len(m.Maps); n > 0 {
		return m.Maps[n-1]
	}
	if n := len(m.List); n > 0 {
		if last, ok := m.List[n-1].(map[string]interface{}); ok {
			return last
		}
	}
	return nil
}

// MarkAnthropicHistoryBreakpoint places a cache_control marker on the last
// content block of the last message so the whole conversation becomes a
// cacheable prefix. It handles every content shape the Messages API allows:
// a plain string (converted to a single text block), a block slice of either
// element type, and adapter content types that marshal to one of those.
// Empty content is left alone — the API rejects empty text blocks — and so
// is a request whose last message is an assistant turn (a prefill), which
// must stay unmarked.
func MarkAnthropicHistoryBreakpoint(messages AnthropicMessages, marker CacheMarker) {
	if marker == nil {
		return
	}
	last := messages.last()
	if last == nil {
		return
	}
	// Stored as the plain map every other marker site uses, so request
	// inspection (tests, the budget pass) sees one shape throughout.
	plain := map[string]string(marker)
	if role, _ := last["role"].(string); !strings.EqualFold(role, "user") {
		return
	}
	switch content := last["content"].(type) {
	case string:
		if strings.TrimSpace(content) == "" {
			return
		}
		last["content"] = []map[string]interface{}{{
			"type":          "text",
			"text":          content,
			"cache_control": plain,
		}}
	case []map[string]interface{}:
		if len(content) == 0 {
			return
		}
		if blk := content[len(content)-1]; blk != nil && markableBlock(blk) {
			blk["cache_control"] = plain
		}
	case []interface{}:
		if len(content) == 0 {
			return
		}
		if blk, ok := content[len(content)-1].(map[string]interface{}); ok && markableBlock(blk) {
			blk["cache_control"] = plain
		}
	default:
		// Adapter-specific content values (the vision wire type that
		// marshals to either a string or a block array) are normalized
		// through their JSON form, marked, and stored back as plain data —
		// the wire bytes are identical, only the marker is added.
		if normalized, ok := normalizeContentJSON(content); ok {
			last["content"] = normalized
			MarkAnthropicHistoryBreakpoint(messages, marker)
		}
	}
}

// normalizeContentJSON round-trips a content value through JSON into the
// plain string / block-list shapes marker logic handles (adapters keep
// dialect-specific content types that marshal to one of those two). ok is
// false for values that do not marshal or produce another shape.
func normalizeContentJSON(content interface{}) (interface{}, bool) {
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, false
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return asString, true
	}
	var asBlocks []interface{}
	if json.Unmarshal(raw, &asBlocks) == nil {
		return asBlocks, true
	}
	return nil, false
}

// markableBlock reports whether a content block may carry cache_control:
// any block type except an empty text block (rejected by the API).
func markableBlock(blk map[string]interface{}) bool {
	if t, _ := blk["type"].(string); t == "text" {
		if txt, _ := blk["text"].(string); strings.TrimSpace(txt) == "" {
			return false
		}
	}
	return true
}
