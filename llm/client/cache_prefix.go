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

// TaskBudgetBeta is the Anthropic beta that enables the task budget: a
// token ceiling the model reads while generating, so it paces itself
// instead of being cut off mid-task.
const TaskBudgetBeta = "task-budgets-2026-03-13"

// TaskBudget is the output_config.task_budget block.
type TaskBudget struct {
	Type string `json:"type"`
	// Total is the ceiling for the whole task.
	Total int `json:"total"`
	// Remaining is how much of it is left. Sent because ChatCLI rewrites
	// history when it compacts, and a server that cannot see the earlier
	// turns cannot derive the spend itself.
	Remaining int `json:"remaining"`
}

// AnthropicTaskBudget builds the task-budget block for the first turn of a
// run, where the ceiling and what is left of it are the same number. Nil
// when there is nothing to say, so callers can assign it without a check.
//
// Later turns must use AnthropicTaskBudgetFor: a run that recomputes both
// numbers every turn shows the model a ceiling that shrinks with the
// spend, which is not what the field means.
func AnthropicTaskBudget(remainingTokens int) *TaskBudget {
	return AnthropicTaskBudgetFor(remainingTokens, remainingTokens)
}

// AnthropicTaskBudgetFor builds the block from a ceiling fixed when the run
// started and what is left of it now.
//
// Remaining is clamped to total. The remaining spend is derived from a
// budget that can grow underneath a run — a daily limit rolls over at
// midnight — and a run whose ceiling grew mid-task would tell the model it
// has more room than it started with, which is the opposite of what a
// budget is for. The ceiling a run announced is the ceiling it keeps.
func AnthropicTaskBudgetFor(total, remaining int) *TaskBudget {
	if total <= 0 || remaining <= 0 {
		return nil
	}
	if remaining > total {
		remaining = total
	}
	return &TaskBudget{Type: "tokens", Total: total, Remaining: remaining}
}

// CompactionBeta is the Anthropic beta that enables server-side
// compaction, where the API summarizes the older conversation itself
// instead of the client spending a turn on a summarizer.
const CompactionBeta = "compact-2026-01-12"

// contextEngine returns the selected engine, lowercased and trimmed.
func contextEngine() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv(ContextEngineEnv)))
}

// ProviderContextEngine reports whether the provider's own context
// management is in play. Both provider values ask for it: the compaction
// engine is the editing engine plus a summarizing edit, never a
// replacement, so a prompt that outgrows the window is still trimmed of
// stale tool results on the way there.
func ProviderContextEngine() bool {
	switch contextEngine() {
	case "provider", "provider-compact":
		return true
	}
	return false
}

// ProviderCompactionEngine reports whether the provider should also
// summarize the older conversation server-side
// (CHATCLI_CONTEXT_ENGINE=provider-compact).
//
// Opt-in and never the default: ChatCLI's own compaction is recoverable —
// what it cuts is archived and can be recalled — and a server-side summary
// is not. The value exists for sessions that would rather not spend a turn
// on a local summarizer.
func ProviderCompactionEngine() bool {
	return contextEngine() == "provider-compact"
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
	edits := []ContextEdit{{
		Type:         "clear_tool_uses_20250919",
		Trigger:      &ContextThreshold{Type: "input_tokens", Value: 100000},
		Keep:         &ContextThreshold{Type: "tool_uses", Value: 5},
		ClearAtLeast: &ContextThreshold{Type: "input_tokens", Value: 20000},
	}}
	// Once reasoning blocks are replayed they occupy the window like any
	// other content, so the engine needs a way to retire the old ones. The
	// thresholds mirror the tool-use edit: same trigger, so one pass over
	// the prompt clears both, and a keep count that leaves the recent
	// reasoning the model is still building on.
	edits = append(edits, ContextEdit{
		Type:    "clear_thinking_20251015",
		Trigger: &ContextThreshold{Type: "input_tokens", Value: 100000},
		Keep:    &ContextThreshold{Type: "thinking_turns", Value: 3},
	})
	// Server-side compaction summarizes what the two clearing edits could
	// not free. It runs last on purpose: clearing stale tool results and
	// old reasoning is lossless, summarizing is not, so the cheap edits
	// get their chance first.
	if ProviderCompactionEngine() {
		edits = append(edits, ContextEdit{
			Type:    "compact_20260112",
			Trigger: &ContextThreshold{Type: "input_tokens", Value: 150000},
		})
	}
	return &ContextManagement{Edits: edits}
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

// at returns message i of whichever list is populated.
func (m AnthropicMessages) at(i int) map[string]interface{} {
	if len(m.Maps) > 0 {
		return m.Maps[i]
	}
	if msg, ok := m.List[i].(map[string]interface{}); ok {
		return msg
	}
	return nil
}

// count is how many messages the request carries.
func (m AnthropicMessages) count() int {
	if n := len(m.Maps); n > 0 {
		return n
	}
	return len(m.List)
}

// lastMarkable returns the final message that can carry the rolling cache
// breakpoint, walking past trailing turn-scoped system messages.
//
// A system message inside messages is a new shape: it did not exist when
// the breakpoint was written, and it rejects cache_control outright. It
// also sits last whenever the current turn carries per-turn context — so
// a marker placed on "the final message" stopped being placed at all, and
// the whole conversation quietly stopped being a cacheable prefix on every
// model that takes the form.
func (m AnthropicMessages) lastMarkable() map[string]interface{} {
	for i := m.count() - 1; i >= 0; i-- {
		msg := m.at(i)
		if msg == nil {
			return nil
		}
		if role, _ := msg["role"].(string); strings.EqualFold(role, "system") {
			continue
		}
		return msg
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
// must stay unmarked. Trailing turn-scoped system messages are walked past
// rather than marked: they reject cache_control, and the breakpoint
// belongs on the turn behind them.
func MarkAnthropicHistoryBreakpoint(messages AnthropicMessages, marker CacheMarker) {
	if marker == nil {
		return
	}
	last := messages.lastMarkable()
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
