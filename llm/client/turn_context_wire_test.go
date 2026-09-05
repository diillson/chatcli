/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"testing"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
)

func turnContext(text string) models.Message {
	return models.TurnContextMessage(text)
}

// TestTurnContextEmitter_OnlyClaimsWhereTheWireTakesIt is the no-regression
// guard. A turn-scoped system message is an Anthropic wire form; every
// provider whose catalog entry does not claim the capability must go on
// carrying the block as the user-role message it has always been.
func TestTurnContextEmitter_OnlyClaimsWhereTheWireTakesIt(t *testing.T) {
	for _, tc := range []struct {
		provider, model string
		want            bool
	}{
		{catalog.ProviderClaudeAI, "claude-opus-5", true},
		{catalog.ProviderClaudeAI, "claude-fable-5-1", true},
		{catalog.ProviderClaudeAI, "claude-opus-4-8", true},
		// Sonnet 5 is explicitly excluded upstream: the top-level system
		// field is the only channel there.
		{catalog.ProviderClaudeAI, "claude-sonnet-5", false},
		// Served on Bedrock, so the mirror takes it too.
		{catalog.ProviderBedrock, "anthropic.claude-opus-5", true},
		{catalog.ProviderBedrock, "anthropic.claude-sonnet-5", false},
		// Every other provider keeps the user-role block.
		{catalog.ProviderOpenAI, "gpt-5.6", false},
		{catalog.ProviderGoogleAI, "gemini-3.1-pro", false},
		{catalog.ProviderXAI, "grok-4", false},
		{catalog.ProviderOpenRouter, "anthropic/claude-opus-5", false},
		{"", "", false},
	} {
		e := NewTurnContextEmitter(tc.provider, tc.model)
		if got := e.Claim(turnContext("today is Friday")); got != tc.want {
			t.Errorf("%s/%s claim = %v, want %v", tc.provider, tc.model, got, tc.want)
		}
	}
}

// TestTurnContextEmitter_ClaimsOnlyTurnContext pins what the emitter is
// allowed to touch: ChatCLI's own per-turn block, never the user's words
// or anything the provider already has a place for.
func TestTurnContextEmitter_ClaimsOnlyTurnContext(t *testing.T) {
	e := NewTurnContextEmitter(catalog.ProviderClaudeAI, "claude-opus-5")
	for _, msg := range []models.Message{
		{Role: "user", Content: "what changed?"},
		{Role: "assistant", Content: "nothing"},
		{Role: "system", Content: "you are a CLI"},
		{Role: "tool", Content: "exit 0", ToolCallID: "t1"},
		models.TurnContextMessage(""), // nothing to say
	} {
		if e.Claim(msg) {
			t.Fatalf("emitter claimed a message it must not touch: %+v", msg)
		}
	}
	// A block carrying images is rendered the old way rather than losing
	// them: a system message takes text only.
	withImage := turnContext("look at this")
	withImage.Images = []models.ImageContent{{}}
	if e.Claim(withImage) {
		t.Fatal("a turn-context block with images must keep the user-role path")
	}
	if e.Used() {
		t.Fatal("nothing was emitted, so no beta may be declared")
	}
}

// TestTurnContextEmitter_DefersByOnePosition covers the placement rule. A
// turn-scoped message renders only while no user message follows it, so a
// block emitted where the history holds it — ahead of the user's turn —
// would clear on the very request that introduced it.
func TestTurnContextEmitter_DefersByOnePosition(t *testing.T) {
	e := NewTurnContextEmitter(catalog.ProviderClaudeAI, "claude-opus-5")
	if !e.Claim(turnContext("today is Friday")) {
		t.Fatal("precondition: the block must be claimed")
	}
	// Nothing is available until the message it accompanies is appended.
	if got := e.Flush(); len(got) != 1 || got[0].Content != "today is Friday" {
		t.Fatalf("flush = %+v, want the claimed block once", got)
	}
	if got := e.Flush(); got != nil {
		t.Fatalf("a block must be emitted exactly once, got %+v", got)
	}
	if !e.Used() {
		t.Fatal("a request that carried a block must declare the beta")
	}

	// Shape: text only, scoped to this turn. cache_control, output_config
	// and tool changes are all rejected on a turn-scoped message, so none
	// of them may appear.
	_ = e.Claim(turnContext("second block"))
	got := e.Flush()
	if got[0].Role != "system" || got[0].ClearAt != ClearAtNextUserMessage {
		t.Fatalf("wire shape = %+v", got[0])
	}
}

// TestTurnContextEmitter_NilSafe covers the callers that pass no emitter
// (the non-tool paths and existing tests): claiming nothing means their
// loops are byte-for-byte what they were.
func TestTurnContextEmitter_NilSafe(t *testing.T) {
	var e *TurnContextEmitter
	if e.Claim(turnContext("x")) || e.Flush() != nil || e.Used() {
		t.Fatal("a nil emitter must be inert")
	}
}
