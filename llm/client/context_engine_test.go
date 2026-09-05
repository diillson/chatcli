/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnthropicContextManagement_OnlyForProviderEngine(t *testing.T) {
	t.Setenv(ContextEngineEnv, "mcp:engine")
	if AnthropicContextManagementFor(200000) != nil || ProviderContextEngine() {
		t.Fatal("only the provider value enables server-side editing")
	}
	t.Setenv(ContextEngineEnv, " Provider ")
	// A model whose window the catalog does not know keeps the fixed
	// thresholds this shipped with.
	cm := AnthropicContextManagementFor(0)
	if cm == nil || !ProviderContextEngine() {
		t.Fatal("provider engine must be on")
	}
	raw, _ := json.Marshal(cm)
	for _, want := range []string{`"edits":[{"type":"clear_tool_uses_20250919"`, `"trigger":{"type":"input_tokens","value":100000}`, `"keep":{"type":"tool_uses","value":5}`, `"clear_at_least":{"type":"input_tokens","value":20000}`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("wire shape missing %s: %s", want, raw)
		}
	}
}

// TestContextEditThresholds_FollowTheWindow covers a number that meant
// opposite things on different models. The catalog spans 128K to 1M, and
// the trigger was the constant 100000 for all of them: four fifths of a
// small window, a tenth of a large one. Half the window and a tenth of it
// mean the same thing everywhere.
func TestContextEditThresholds_FollowTheWindow(t *testing.T) {
	for _, tc := range []struct{ window, trigger, release int }{
		{1000000, 500000, 100000},
		{200000, 100000, 20000},
		{128000, 64000, 12800},
	} {
		trigger, release := contextEditThresholds(tc.window)
		if trigger != tc.trigger || release != tc.release {
			t.Errorf("window %d → trigger %d release %d, want %d and %d", tc.window, trigger, release, tc.trigger, tc.release)
		}
		if trigger >= tc.window {
			t.Errorf("window %d: clearing must start before the window is full", tc.window)
		}
	}

	// An unknown window keeps the values this shipped with, so a model the
	// catalog does not carry behaves exactly as before.
	if trigger, release := contextEditThresholds(0); trigger != defaultContextEditTrigger || release != defaultContextEditRelease {
		t.Errorf("unknown window → %d / %d, want the shipped defaults", trigger, release)
	}

	// A very small window still gets room to work before anything clears.
	if trigger, _ := contextEditThresholds(8000); trigger < 8000 {
		t.Errorf("a small window must not clear immediately, trigger %d", trigger)
	}

	// Summarizing waits for the lossless edits: its trigger sits above.
	t.Setenv(ContextEngineEnv, "provider-compact")
	cm := AnthropicContextManagementFor(200000)
	var clearAt, compactAt int
	for _, e := range cm.Edits {
		switch e.Type {
		case "clear_tool_uses_20250919":
			clearAt = e.Trigger.Value
		case "compact_20260112":
			compactAt = e.Trigger.Value
		}
	}
	if compactAt <= clearAt {
		t.Fatalf("summarizing must trigger after clearing: compact %d vs clear %d", compactAt, clearAt)
	}
}
