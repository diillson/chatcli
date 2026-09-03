/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"encoding/json"
	"testing"
)

func jsonMarshal(v interface{}) ([]byte, error) { return json.Marshal(v) }

// wrap builds the AnthropicMessages carrier for either test slice shape.
func wrap(v interface{}) AnthropicMessages {
	switch s := v.(type) {
	case []map[string]interface{}:
		return AnthropicMessages{Maps: s}
	case []interface{}:
		return AnthropicMessages{List: s}
	}
	return AnthropicMessages{}
}

func TestAnthropicCacheTTL_Normalizes(t *testing.T) {
	cases := map[string]string{"": "5m", "5m": "5m", "1h": "1h", "1H": "1h", "hour": "1h", "banana": "5m", "2h": "5m"}
	for in, want := range cases {
		t.Setenv(PromptCacheTTLEnv, in)
		if got := AnthropicCacheTTL(); got != want {
			t.Fatalf("ttl(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestAnthropicCacheMarker_TTLField(t *testing.T) {
	t.Setenv(PromptCacheTTLEnv, "")
	if m := AnthropicCacheMarker(); m["type"] != "ephemeral" || m["ttl"] != "" {
		t.Fatalf("default marker = %v", m)
	}
	t.Setenv(PromptCacheTTLEnv, "1h")
	if m := AnthropicCacheMarker(); m["ttl"] != "1h" {
		t.Fatalf("1h marker = %v", m)
	}
	if m := AnthropicCacheMarkerWithTTL(false); m["ttl"] != "" {
		t.Fatalf("adapters without ttl support must get the plain marker, got %v", m)
	}
}

func TestMarkHistoryBreakpoint_StringContentBecomesBlock(t *testing.T) {
	marker := map[string]string{"type": "ephemeral"}
	msgs := []map[string]interface{}{
		{"role": "user", "content": "hi"},
		{"role": "assistant", "content": "hello"},
		{"role": "user", "content": "do the thing"},
	}
	MarkAnthropicHistoryBreakpoint(wrap(msgs), marker)
	blocks, ok := msgs[2]["content"].([]map[string]interface{})
	if !ok || len(blocks) != 1 {
		t.Fatalf("last content should be one text block, got %#v", msgs[2]["content"])
	}
	if blocks[0]["text"] != "do the thing" || blocks[0]["cache_control"] == nil {
		t.Fatalf("block = %#v", blocks[0])
	}
	if _, isStr := msgs[0]["content"].(string); !isStr {
		t.Fatal("earlier messages must be untouched")
	}
}

func TestMarkHistoryBreakpoint_BlockSlices(t *testing.T) {
	marker := map[string]string{"type": "ephemeral"}
	// tool_result shape ([]map) produced by the tool-use builder.
	toolMsgs := []interface{}{
		map[string]interface{}{"role": "assistant", "content": "calling"},
		map[string]interface{}{"role": "user", "content": []map[string]interface{}{
			{"type": "tool_result", "tool_use_id": "t1", "content": "bytes"},
		}},
	}
	MarkAnthropicHistoryBreakpoint(wrap(toolMsgs), marker)
	blk := toolMsgs[1].(map[string]interface{})["content"].([]map[string]interface{})[0]
	if blk["cache_control"] == nil {
		t.Fatalf("tool_result block not marked: %#v", blk)
	}

	// vision shape ([]interface{}) — last block marked.
	visionMsgs := []map[string]interface{}{
		{"role": "user", "content": []interface{}{
			map[string]interface{}{"type": "image", "source": map[string]interface{}{}},
			map[string]interface{}{"type": "text", "text": "what is this?"},
		}},
	}
	MarkAnthropicHistoryBreakpoint(wrap(visionMsgs), marker)
	blocks := visionMsgs[0]["content"].([]interface{})
	if blocks[1].(map[string]interface{})["cache_control"] == nil {
		t.Fatal("last vision block not marked")
	}
	if blocks[0].(map[string]interface{})["cache_control"] != nil {
		t.Fatal("only the last block may carry the marker")
	}
}

func TestMarkHistoryBreakpoint_SkipsPrefillAndEmpty(t *testing.T) {
	marker := map[string]string{"type": "ephemeral"}
	prefill := []map[string]interface{}{
		{"role": "user", "content": "q"},
		{"role": "assistant", "content": "{"},
	}
	MarkAnthropicHistoryBreakpoint(wrap(prefill), marker)
	if _, isStr := prefill[1]["content"].(string); !isStr {
		t.Fatal("assistant prefill must stay unmarked")
	}
	empty := []map[string]interface{}{{"role": "user", "content": "   "}}
	MarkAnthropicHistoryBreakpoint(wrap(empty), marker)
	if _, isStr := empty[0]["content"].(string); !isStr {
		t.Fatal("empty user content must not become an empty marked block")
	}
	emptyBlock := []map[string]interface{}{{"role": "user", "content": []map[string]interface{}{{"type": "text", "text": ""}}}}
	MarkAnthropicHistoryBreakpoint(wrap(emptyBlock), marker)
	if emptyBlock[0]["content"].([]map[string]interface{})[0]["cache_control"] != nil {
		t.Fatal("empty text block must not be marked")
	}
	MarkAnthropicHistoryBreakpoint(AnthropicMessages{}, marker)
	MarkAnthropicHistoryBreakpoint(AnthropicMessages{Maps: []map[string]interface{}{}}, marker)
	MarkAnthropicHistoryBreakpoint(wrap(prefill), nil)
}

// jsonContent mimics adapter content types that marshal to a string or a
// block array (the vision wire type) — the marker must see through them.
type jsonContent struct{ v interface{} }

func (j jsonContent) MarshalJSON() ([]byte, error) { return jsonMarshal(j.v) }

func TestMarkHistoryBreakpoint_CustomMarshalerContent(t *testing.T) {
	marker := map[string]string{"type": "ephemeral"}
	msgs := []map[string]interface{}{{"role": "user", "content": jsonContent{"hi"}}}
	MarkAnthropicHistoryBreakpoint(wrap(msgs), marker)
	blocks, ok := msgs[0]["content"].([]map[string]interface{})
	if !ok || blocks[0]["cache_control"] == nil || blocks[0]["text"] != "hi" {
		t.Fatalf("string-marshaling content not marked: %#v", msgs[0]["content"])
	}
	arr := []map[string]interface{}{{"role": "user", "content": jsonContent{[]interface{}{
		map[string]interface{}{"type": "image", "source": map[string]interface{}{}},
		map[string]interface{}{"type": "text", "text": "what"},
	}}}}
	MarkAnthropicHistoryBreakpoint(wrap(arr), marker)
	parts, ok := arr[0]["content"].([]interface{})
	if !ok || parts[1].(map[string]interface{})["cache_control"] == nil {
		t.Fatalf("array-marshaling content not marked: %#v", arr[0]["content"])
	}
}
