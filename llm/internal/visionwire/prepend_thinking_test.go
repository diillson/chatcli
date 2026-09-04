package visionwire

import (
	"encoding/json"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestPrependThinkingLeadsTheTurn(t *testing.T) {
	c := AnthropicContent("running it", nil).PrependThinking([]models.ThinkingBlock{
		{Type: "thinking", Thinking: "weighing", Signature: "sig"},
		{Type: "redacted_thinking", Data: "enc"},
	})
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("want the array form once reasoning leads: %v (%s)", err, raw)
	}
	if len(blocks) != 3 {
		t.Fatalf("want thinking+redacted+text, got %d: %s", len(blocks), raw)
	}
	if blocks[0]["type"] != "thinking" || blocks[1]["type"] != "redacted_thinking" || blocks[2]["type"] != "text" {
		t.Errorf("block order wrong: %s", raw)
	}
}

func TestPrependThinkingIsByteIdenticalWithoutBlocks(t *testing.T) {
	base, _ := json.Marshal(AnthropicContent("plain answer", nil))
	with, _ := json.Marshal(AnthropicContent("plain answer", nil).PrependThinking(nil))
	if string(base) != string(with) {
		t.Errorf("a turn with no reasoning must keep its wire bytes: %s vs %s", base, with)
	}
	unsigned, _ := json.Marshal(AnthropicContent("plain answer", nil).
		PrependThinking([]models.ThinkingBlock{{Type: "thinking", Thinking: "x"}}))
	if string(base) != string(unsigned) {
		t.Errorf("an unsigned block must be skipped, not sent: %s", unsigned)
	}
}
