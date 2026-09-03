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
	if AnthropicContextManagement() != nil || ProviderContextEngine() {
		t.Fatal("only the provider value enables server-side editing")
	}
	t.Setenv(ContextEngineEnv, " Provider ")
	cm := AnthropicContextManagement()
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
