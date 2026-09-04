package cli

import (
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

type fakeThinkingClient struct {
	client.LLMClient
	model  string
	blocks []models.ThinkingBlock
	from   string
}

func (f *fakeThinkingClient) GetModelName() string                 { return f.model }
func (f *fakeThinkingClient) LastThinking() []models.ThinkingBlock { return f.blocks }
func (f *fakeThinkingClient) LastThinkingModel() string            { return f.from }

func TestTurnThinkingBlocksPrefersTheResponse(t *testing.T) {
	resp := &models.LLMResponse{Thinking: []models.ThinkingBlock{{Type: "thinking", Thinking: "from resp", Signature: "s"}}}
	c := &fakeThinkingClient{model: "m", from: "m", blocks: []models.ThinkingBlock{{Type: "thinking", Thinking: "stale", Signature: "s"}}}
	got := turnThinkingBlocks(resp, c)
	if len(got) != 1 || got[0].Thinking != "from resp" {
		t.Errorf("structured response must win, got %+v", got)
	}
}

func TestTurnThinkingBlocksFallsBackToTheClient(t *testing.T) {
	c := &fakeThinkingClient{model: "m", from: "m", blocks: []models.ThinkingBlock{{Type: "thinking", Thinking: "plain path", Signature: "s"}}}
	got := turnThinkingBlocks(nil, c)
	if len(got) != 1 || got[0].Thinking != "plain path" {
		t.Errorf("plain path blocks must be picked up, got %+v", got)
	}
}

// Reasoning blocks are bound to the model that produced them: replaying
// another model's blocks is rejected, so a route change contributes none.
func TestTurnThinkingBlocksRefusesForeignModel(t *testing.T) {
	c := &fakeThinkingClient{model: "sonnet", from: "opus", blocks: []models.ThinkingBlock{{Type: "thinking", Thinking: "x", Signature: "s"}}}
	if got := turnThinkingBlocks(nil, c); got != nil {
		t.Errorf("blocks from another model must not replay, got %+v", got)
	}
}

func TestTurnThinkingBlocksHandlesPlainClient(t *testing.T) {
	if got := turnThinkingBlocks(nil, nil); got != nil {
		t.Errorf("a client without the capability must yield nil, got %+v", got)
	}
}
