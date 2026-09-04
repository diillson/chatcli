package bedrock

import (
	"encoding/json"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// Bedrock's Anthropic surface enables extended thinking too, so it owes
// the same replay: the reasoning that produced a turn travels back with it.
func TestBedrockReplaysThinkingOnAssistantTurn(t *testing.T) {
	c := NewBedrockClient("anthropic.claude-opus-5", "us-east-1", "", zap.NewNop(), 1, 0)
	msgs, _ := c.buildMessagesAndSystem("", []models.Message{
		{Role: "user", Content: "go"},
		{
			Role:     "assistant",
			Content:  "done",
			Thinking: []models.ThinkingBlock{{Type: "thinking", Thinking: "weighing", Signature: "sig"}},
		},
	})
	raw, err := json.Marshal(msgs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, m := range decoded {
		if m.Role != "assistant" {
			continue
		}
		var blocks []map[string]any
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			t.Fatalf("assistant turn should carry a block array: %v (%s)", err, m.Content)
		}
		if len(blocks) == 0 || blocks[0]["type"] != "thinking" || blocks[0]["signature"] != "sig" {
			t.Fatalf("reasoning must lead the assistant turn: %s", m.Content)
		}
		return
	}
	t.Fatal("no assistant turn in the payload")
}

func TestBedrockStoreThinkingFromBody(t *testing.T) {
	c := NewBedrockClient("anthropic.claude-opus-5", "us-east-1", "", zap.NewNop(), 1, 0)
	c.storeThinkingFromBody([]byte(`{"content":[{"type":"thinking","thinking":"a","signature":"s"},{"type":"text","text":"b"}]}`))
	if got := c.LastThinking(); len(got) != 1 || got[0].Signature != "s" {
		t.Fatalf("blocks not stored: %+v", got)
	}
	if c.LastThinkingModel() != "anthropic.claude-opus-5" {
		t.Errorf("blocks must remember their model, got %q", c.LastThinkingModel())
	}
	c.storeThinkingFromBody([]byte("garbage"))
	if got := c.LastThinking(); got != nil {
		t.Errorf("an unparseable body must clear, not keep stale blocks: %+v", got)
	}
}
