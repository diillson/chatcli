/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// rerankFakeClient captures the rerank call.
type rerankFakeClient struct {
	prompt string
	hist   []models.Message
}

func (f *rerankFakeClient) GetModelName() string { return "fake" }
func (f *rerankFakeClient) SendPrompt(_ context.Context, p string, hist []models.Message, _ int) (string, error) {
	f.prompt, f.hist = p, append([]models.Message(nil), hist...)
	return "2,1", nil
}

func TestRerankPromptFunc_SystemOnlyPromptAndTaggedUsage(t *testing.T) {
	fake := &rerankFakeClient{}
	c := &ChatCLI{logger: zap.NewNop(), Client: fake, Provider: "OPENAI", Model: "gpt-5.6", costTracker: NewCostTracker()}
	out, err := c.rerankPromptFunc()(context.Background(), "RANK THESE")
	if err != nil || out != "2,1" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if len(fake.hist) != 2 || fake.hist[0].Role != "system" || fake.hist[0].Content != "RANK THESE" || fake.hist[1].Role != "user" || fake.hist[1].Content != rerankUserTurn {
		t.Fatalf("the instruction rides once as the system message: %+v", fake.hist)
	}
	if fake.prompt != rerankUserTurn {
		t.Fatalf("prompt must be the fixed user turn, got %q", fake.prompt)
	}
	if calls, _ := c.costTracker.MemoryStats(); calls != 1 {
		t.Fatalf("rerank usage must be booked as background spend: %d", calls)
	}
	// No client at all: unavailable.
	if _, err := (&ChatCLI{logger: zap.NewNop()}).rerankPromptFunc()(context.Background(), "x"); err == nil {
		t.Fatal("no client → ErrRerankUnavailable")
	}
}
