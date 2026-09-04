/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"encoding/json"
	"sync"

	"github.com/diillson/chatcli/models"
)

// Provider-native reasoning blocks.
//
// When extended thinking is on, Anthropic returns the model's reasoning as
// its own content blocks ahead of the text and tool_use blocks, and the
// contract for continuing the same conversation is to send those blocks
// back unchanged inside the assistant turn that carries the tool_use. A
// client that parses only text and tool_use pays for the reasoning, drops
// it, and makes every step of an agent loop start its reasoning over.
//
// The blocks are opaque: `thinking` carries text plus a signature that the
// provider verifies, and `redacted_thinking` carries an encrypted payload
// with no readable text at all. Neither may be edited, summarized or
// re-ordered — they are replayed verbatim or not at all.

// ThinkingState is an embeddable holder that gives a provider client the
// LastThinking accessor, mirroring how UsageState provides LastUsage. The
// plain (non-tool) send paths return a string, so the blocks they parse
// have nowhere to ride out on; the caller reads them from here instead.
type ThinkingState struct {
	mu            sync.RWMutex
	lastThinking  []models.ThinkingBlock
	thinkingModel string
}

// StoreThinking saves the reasoning blocks of the most recent response,
// together with the model that produced them. Passing nil clears the
// state, which every send path must do before a request so a response
// without reasoning never replays the previous turn's blocks.
func (s *ThinkingState) StoreThinking(model string, blocks []models.ThinkingBlock) {
	s.mu.Lock()
	s.lastThinking = blocks
	s.thinkingModel = model
	s.mu.Unlock()
}

// LastThinking returns the reasoning blocks of the most recent response.
// Implements ThinkingAwareClient.
func (s *ThinkingState) LastThinking() []models.ThinkingBlock {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastThinking
}

// LastThinkingModel returns the model that produced the blocks LastThinking
// reports. Reasoning blocks are bound to their producing model, so a caller
// that switched models mid-session must not replay them.
func (s *ThinkingState) LastThinkingModel() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.thinkingModel
}

// ThinkingAwareClient is the optional capability of a client that can hand
// back the provider-native reasoning blocks of its last response. Callers
// must keep working when a client does not implement it.
type ThinkingAwareClient interface {
	LLMClient

	// LastThinking returns the reasoning blocks of the most recent
	// response, or nil when the response carried none.
	LastThinking() []models.ThinkingBlock

	// LastThinkingModel names the model those blocks came from.
	LastThinkingModel() string
}

// AsThinkingAware returns the client as a ThinkingAwareClient when it is one.
func AsThinkingAware(c LLMClient) (ThinkingAwareClient, bool) {
	tc, ok := c.(ThinkingAwareClient)
	return tc, ok
}

// AnthropicThinkingWire carries reasoning blocks already shaped for the
// Anthropic request body. It exists so the builder can return wire maps
// without putting a bare map-of-interface in an exported signature.
type AnthropicThinkingWire struct {
	Blocks []map[string]interface{}
}

// ParseAnthropicThinkingBody extracts the reasoning blocks from a raw
// Anthropic messages response. Callers pass the response bytes they
// already hold: the typed parsers on the provider paths read only text
// and tool_use, so this walks the content array once rather than widening
// them. A body that does not parse yields no blocks — never the previous
// turn's.
func ParseAnthropicThinkingBody(body []byte) []models.ThinkingBlock {
	var generic struct {
		Content []interface{} `json:"content"`
	}
	if err := json.Unmarshal(body, &generic); err != nil {
		return nil
	}
	return parseAnthropicThinkingBlocks(generic.Content)
}

// parseAnthropicThinkingBlocks reads one already-decoded content array.
// Unknown block types are ignored; a thinking block missing its signature
// is dropped, because replaying an unsigned block is rejected by the
// provider and silently losing the turn is worse than losing the
// reasoning.
func parseAnthropicThinkingBlocks(contentBlocks []interface{}) []models.ThinkingBlock {
	var out []models.ThinkingBlock
	for _, raw := range contentBlocks {
		b, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		switch t, _ := b["type"].(string); t {
		case "thinking":
			text, _ := b["thinking"].(string)
			sig, _ := b["signature"].(string)
			if sig == "" {
				continue
			}
			out = append(out, models.ThinkingBlock{Type: "thinking", Thinking: text, Signature: sig})
		case "redacted_thinking":
			data, _ := b["data"].(string)
			if data == "" {
				continue
			}
			out = append(out, models.ThinkingBlock{Type: "redacted_thinking", Data: data})
		}
	}
	return out
}

// AnthropicThinkingBlocks shapes reasoning blocks back into the wire form
// the Anthropic messages API expects. There is no toggle: the blocks only
// exist when the turn asked for extended thinking, and replaying them is
// what that request commits to — a caller that wants none asks for no
// effort instead. The result is empty when there is nothing to replay, so
// callers can prepend it unconditionally.
func AnthropicThinkingBlocks(blocks []models.ThinkingBlock) AnthropicThinkingWire {
	if len(blocks) == 0 {
		return AnthropicThinkingWire{}
	}
	wire := AnthropicThinkingWire{Blocks: make([]map[string]interface{}, 0, len(blocks))}
	for _, b := range blocks {
		switch b.Type {
		case "thinking":
			if b.Signature == "" {
				continue
			}
			wire.Blocks = append(wire.Blocks, map[string]interface{}{
				"type":      "thinking",
				"thinking":  b.Thinking,
				"signature": b.Signature,
			})
		case "redacted_thinking":
			if b.Data == "" {
				continue
			}
			wire.Blocks = append(wire.Blocks, map[string]interface{}{
				"type": "redacted_thinking",
				"data": b.Data,
			})
		}
	}
	return wire
}
