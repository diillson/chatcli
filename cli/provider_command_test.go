/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"testing"

	prompt "github.com/c-bata/go-prompt"
	"go.uber.org/zap"
)

// TestProviderDefaultModel checks that every provider the picker can switch to
// resolves to a concrete default model, and that an unknown provider yields ""
// (letting the client choose). This guards the /switch and /provider paths,
// which share applyProviderSwitch → providerDefaultModel.
func TestProviderDefaultModel(t *testing.T) {
	c := &ChatCLI{}
	known := []string{
		"OPENAI", "CLAUDEAI", "OPENAI_ASSISTANT", "GOOGLEAI", "XAI", "ZAI",
		"MINIMAX", "MOONSHOT", "OLLAMA", "COPILOT", "GITHUB_MODELS",
	}
	for _, p := range known {
		if got := c.providerDefaultModel(p); got == "" {
			t.Errorf("providerDefaultModel(%q) = \"\", want a default model", p)
		}
	}
	if got := c.providerDefaultModel("NOPE"); got != "" {
		t.Errorf("providerDefaultModel(unknown) = %q, want \"\"", got)
	}
}

// newProviderTestCLI builds a CLI backed by the package's minimalManager fake
// (defined in skill_model_resolve_integration_test.go).
func newProviderTestCLI(provider string, providers []string) *ChatCLI {
	return &ChatCLI{
		logger:   zap.NewNop(),
		manager:  &minimalManager{providers: providers},
		Provider: provider,
		Model:    "m0",
		Client:   &fakeClient{provider: provider, model: "m0"},
	}
}

func paletteDoc(line string) prompt.Document {
	b := prompt.NewBuffer()
	b.InsertText(line, false, true)
	return *b.Document()
}

func TestGetProviderSuggestions(t *testing.T) {
	cli := newProviderTestCLI("OPENAI", []string{"OPENAI", "CLAUDEAI", "XAI"})

	if got := cli.getProviderSuggestions(paletteDoc("/provider ")); len(got) != 3 {
		t.Fatalf("bare /provider suggestions = %d, want 3", len(got))
	}
	got := cli.getProviderSuggestions(paletteDoc("/provider CL"))
	if len(got) != 1 || got[0].Text != "CLAUDEAI" {
		t.Errorf("prefix CL = %v, want [CLAUDEAI]", got)
	}
	// Once a provider is chosen there is nothing more to complete.
	if got := cli.getProviderSuggestions(paletteDoc("/provider OPENAI ")); len(got) != 0 {
		t.Errorf("after a provider is chosen, want 0 suggestions, got %d", len(got))
	}
}

func TestHandleProviderCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate the runtime-model state file write

	cli := newProviderTestCLI("OPENAI", []string{"OPENAI", "CLAUDEAI"})

	cli.handleProviderCommand(context.Background(), "/provider claudeai") // case-insensitive switch
	if cli.Provider != "CLAUDEAI" {
		t.Fatalf("provider = %q, want CLAUDEAI", cli.Provider)
	}
	cli.handleProviderCommand(context.Background(), "/provider NOPE") // unknown → unchanged
	if cli.Provider != "CLAUDEAI" {
		t.Errorf("unknown provider changed it to %q", cli.Provider)
	}
	cli.handleProviderCommand(context.Background(), "/provider") // bare lists, leaves it unchanged
	if cli.Provider != "CLAUDEAI" {
		t.Errorf("bare /provider changed it to %q", cli.Provider)
	}
}

func TestCommandIsPickable(t *testing.T) {
	cli := newProviderTestCLI("OPENAI", []string{"OPENAI", "CLAUDEAI"})

	if !cli.commandIsPickable("/provider") {
		t.Error("/provider should be pickable — it offers concrete provider options")
	}
	for _, m := range []string{"/agent", "/run", "/coder", "/plan"} {
		if cli.commandIsPickable(m) {
			t.Errorf("mode-switch %q must never be pickable", m)
		}
	}
}

func TestRunRequestedPaletteNoop(t *testing.T) {
	cli := newProviderTestCLI("OPENAI", []string{"OPENAI"})

	// No request pending → returns immediately.
	if cli.runRequestedPalette() {
		t.Error("runRequestedPalette returned exit=true with no request")
	}
	// Requested, but the test env is not a TTY → the overlay is skipped, the
	// flag is cleared, and it does not ask to exit.
	cli.paletteRequested = true
	cli.paletteTarget = "/provider"
	if cli.runRequestedPalette() {
		t.Error("runRequestedPalette returned exit=true outside a TTY")
	}
	if cli.paletteRequested {
		t.Error("paletteRequested was not cleared")
	}
}
