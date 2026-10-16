/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package fallback

import (
	"context"
	"errors"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

// parentClient records the pulse parent each request was made under.
type parentClient struct {
	mockToolClient
	parents []string
}

func (p *parentClient) SendPrompt(ctx context.Context, prompt string, h []models.Message, n int) (string, error) {
	p.parents = append(p.parents, pulse.ParentFrom(ctx))
	return p.mockClient.SendPrompt(ctx, prompt, h, n)
}

func (p *parentClient) SendPromptWithTools(ctx context.Context, prompt string, h []models.Message, tools []models.ToolDefinition, n int) (*models.LLMResponse, error) {
	p.parents = append(p.parents, pulse.ParentFrom(ctx))
	return p.mockToolClient.SendPromptWithTools(ctx, prompt, h, tools, n)
}

func TestChainReportsAttemptsAndWhoServed(t *testing.T) {
	rec := pulsetest.Record(t)
	primary := &parentClient{mockToolClient: mockToolClient{mockClient: mockClient{model: "gpt-4", err: errors.New("401 unauthorized")}}}
	secondary := &parentClient{mockToolClient: mockToolClient{mockClient: mockClient{model: "claude-3", response: "ok"}}}
	chain := NewChain(newTestLogger(), []FallbackEntry{
		{Provider: "OPENAI", Model: "gpt-4", Client: primary},
		{Provider: "CLAUDEAI", Model: "claude-3", Client: secondary},
	})

	if _, err := chain.SendPrompt(context.Background(), "hi", nil, 100); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	end := rec.WaitFor(t, "fallback end", pulsetest.Is(pulse.KindPattern, pulseFallbackName, pulse.PhaseEnd))
	if end.Status != pulse.StatusOK || end.Attrs["served_by"] != "claudeai:claude-3" || end.Attrs["attempts"] != "2" ||
		end.Attrs["op"] != "send" || end.Attrs["entries"] != "2" {
		t.Fatalf("end = %+v", end)
	}
	upd := rec.WaitFor(t, "failed attempt", pulsetest.Is(pulse.KindPattern, pulseFallbackName, pulse.PhaseUpdate))
	if upd.Attrs["provider"] != "openai" || upd.Attrs["error_class"] != ErrorClassAuth.String() || upd.Attrs["attempt"] != "1" {
		t.Fatalf("update = %+v", upd)
	}
	if len(secondary.parents) != 1 || secondary.parents[0] != end.ID {
		t.Fatalf("the provider's request must hang from the chain's node %q, got %v", end.ID, secondary.parents)
	}

	// The failed provider now cools down: the next call skips it.
	if _, err := chain.SendPromptWithTools(context.Background(), "hi", nil, nil, 100); err != nil {
		t.Fatalf("SendPromptWithTools: %v", err)
	}
	tools := rec.WaitFor(t, "tools end", func(ev pulse.Event) bool {
		return ev.Kind == pulse.KindPattern && ev.Phase == pulse.PhaseEnd && ev.Attrs["op"] == "tools"
	})
	if tools.Attrs["skipped"] != "1" || tools.Attrs["attempts"] != "1" {
		t.Fatalf("tools end = %+v", tools)
	}
}

func TestChainReportsAFailedStream(t *testing.T) {
	rec := pulsetest.Record(t)
	only := &mockClient{model: "m", err: errors.New("model not found")}
	chain := NewChain(newTestLogger(), []FallbackEntry{makeEntry("XAI", "m", only)})
	if _, err := chain.SendPromptStream(context.Background(), "hi", nil, 10); err == nil {
		t.Fatal("expected the stream to fail")
	}
	end := rec.WaitFor(t, "stream end", pulsetest.Is(pulse.KindPattern, pulseFallbackName, pulse.PhaseEnd))
	if end.Status != pulse.StatusError || end.Attrs["op"] != "stream" || end.Attrs["attempts"] != "1" {
		t.Fatalf("end = %+v", end)
	}
}

func TestChainTraceIsFreeWhileOff(t *testing.T) {
	var tr *chainTrace
	tr.skip()
	tr.fail("p", 0, ErrorClassServerError)
	tr.served(FallbackEntry{})
	tr.end(nil)
}
