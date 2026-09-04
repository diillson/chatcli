package client

import (
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestParseAnthropicThinkingKeepsOrderAndDropsUnsigned(t *testing.T) {
	blocks := ParseAnthropicThinkingBody([]byte(`{"content":[
		{"type":"thinking","thinking":"first","signature":"sig-1"},
		{"type":"thinking","thinking":"unsigned"},
		{"type":"redacted_thinking","data":"enc"},
		{"type":"redacted_thinking"},
		{"type":"text","text":"answer"},
		{"type":"tool_use","id":"t1"},
		"not-a-block"
	]}`))
	if len(blocks) != 2 {
		t.Fatalf("want 2 replayable blocks, got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Type != "thinking" || blocks[0].Thinking != "first" || blocks[0].Signature != "sig-1" {
		t.Errorf("first block not carried verbatim: %+v", blocks[0])
	}
	if blocks[1].Type != "redacted_thinking" || blocks[1].Data != "enc" {
		t.Errorf("redacted block not carried verbatim: %+v", blocks[1])
	}
}

func TestParseAnthropicThinkingBodyRejectsGarbage(t *testing.T) {
	if got := ParseAnthropicThinkingBody([]byte("not json")); got != nil {
		t.Errorf("an unparseable body must yield no blocks, got %+v", got)
	}
	if got := ParseAnthropicThinkingBody([]byte(`{"content":[]}`)); got != nil {
		t.Errorf("want nil for no content, got %+v", got)
	}
}

func TestAnthropicThinkingBlocksShapesWire(t *testing.T) {
	wire := AnthropicThinkingBlocks([]models.ThinkingBlock{
		{Type: "thinking", Thinking: "why", Signature: "sig"},
		{Type: "thinking", Thinking: "no sig"},
		{Type: "redacted_thinking", Data: "enc"},
	})
	if len(wire.Blocks) != 2 {
		t.Fatalf("want 2 wire blocks, got %d", len(wire.Blocks))
	}
	if wire.Blocks[0]["signature"] != "sig" || wire.Blocks[0]["thinking"] != "why" {
		t.Errorf("thinking block lost a field: %+v", wire.Blocks[0])
	}
	if wire.Blocks[1]["type"] != "redacted_thinking" || wire.Blocks[1]["data"] != "enc" {
		t.Errorf("redacted block lost a field: %+v", wire.Blocks[1])
	}
}

func TestThinkingStateStoreAndClear(t *testing.T) {
	var st ThinkingState
	if got := st.LastThinking(); got != nil {
		t.Fatalf("zero value must be empty, got %+v", got)
	}
	st.StoreThinking("claude-opus-5", []models.ThinkingBlock{{Type: "thinking", Thinking: "a", Signature: "s"}})
	if len(st.LastThinking()) != 1 || st.LastThinkingModel() != "claude-opus-5" {
		t.Fatalf("store did not round-trip: %+v / %q", st.LastThinking(), st.LastThinkingModel())
	}
	st.StoreThinking("claude-opus-5", nil)
	if st.LastThinking() != nil {
		t.Errorf("a response without reasoning must not replay the previous turn's blocks")
	}
}

// Once reasoning blocks are replayed they occupy the window like any other
// content, so the provider engine must be able to retire the old ones.
func TestAnthropicContextManagementClearsThinking(t *testing.T) {
	t.Setenv(ContextEngineEnv, "provider")
	cm := AnthropicContextManagement()
	if cm == nil {
		t.Fatal("provider engine selected but no context_management block")
	}
	var sawToolUses, sawThinking bool
	for _, e := range cm.Edits {
		switch e.Type {
		case "clear_tool_uses_20250919":
			sawToolUses = true
		case "clear_thinking_20251015":
			sawThinking = true
			if e.Keep == nil || e.Keep.Value <= 0 {
				t.Errorf("thinking edit must keep recent turns, got %+v", e.Keep)
			}
		}
	}
	if !sawToolUses || !sawThinking {
		t.Errorf("want both edits, got %+v", cm.Edits)
	}
}

func TestAnthropicContextManagementOffByDefault(t *testing.T) {
	t.Setenv(ContextEngineEnv, "")
	if cm := AnthropicContextManagement(); cm != nil {
		t.Errorf("builtin engine must send no context_management, got %+v", cm)
	}
}

// The compaction engine is the editing engine plus a summarizing edit,
// never a replacement: a prompt on its way to the window limit is still
// trimmed of stale tool results and old reasoning first, because those
// edits are lossless and a summary is not.
func TestProviderCompactionEngineAddsCompactLast(t *testing.T) {
	t.Setenv(ContextEngineEnv, "provider-compact")
	if !ProviderContextEngine() {
		t.Fatal("the compaction engine must also enable context editing")
	}
	if !ProviderCompactionEngine() {
		t.Fatal("provider-compact must select server-side compaction")
	}
	cm := AnthropicContextManagement()
	if cm == nil || len(cm.Edits) != 3 {
		t.Fatalf("want three edits, got %+v", cm)
	}
	if cm.Edits[len(cm.Edits)-1].Type != "compact_20260112" {
		t.Errorf("summarizing must run after the lossless edits: %+v", cm.Edits)
	}
}

func TestProviderEngineWithoutCompaction(t *testing.T) {
	t.Setenv(ContextEngineEnv, "provider")
	if ProviderCompactionEngine() {
		t.Error("plain provider must not ask the server to summarize")
	}
	cm := AnthropicContextManagement()
	if cm == nil {
		t.Fatal("plain provider still edits context")
	}
	for _, e := range cm.Edits {
		if e.Type == "compact_20260112" {
			t.Errorf("unexpected compaction edit: %+v", cm.Edits)
		}
	}
}

func TestBuiltinEngineSelectsNothing(t *testing.T) {
	t.Setenv(ContextEngineEnv, "builtin")
	if ProviderContextEngine() || ProviderCompactionEngine() {
		t.Error("builtin must ask the provider for nothing")
	}
}
