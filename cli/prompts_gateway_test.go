/*
 * ChatCLI - tests for the conversational gateway system prompt.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"
)

func TestCoderBaseSystemPrompt(t *testing.T) {
	if got := coderBaseSystemPrompt(false); got != CoderSystemPrompt {
		t.Error("non-gateway must use CoderSystemPrompt")
	}
	if got := coderBaseSystemPrompt(true); got != GatewaySystemPrompt {
		t.Error("gateway must use GatewaySystemPrompt")
	}
}

func TestGatewaySystemPrompt_Content(t *testing.T) {
	if GatewaySystemPrompt == "" || GatewaySystemPrompt == CoderSystemPrompt {
		t.Fatal("GatewaySystemPrompt must be a distinct, non-empty prompt")
	}
	// Keeps the tool mechanics...
	for _, want := range []string{"@coder", "tool_call", "@webfetch"} {
		if !strings.Contains(GatewaySystemPrompt, want) {
			t.Errorf("GatewaySystemPrompt missing tool mechanic %q", want)
		}
	}
	// ...but drops the terse coder framing in favor of a conversational voice.
	if strings.Contains(GatewaySystemPrompt, "senior software engineer") {
		t.Error("GatewaySystemPrompt should not carry the coder 'senior software engineer' framing")
	}
	if !strings.Contains(strings.ToLower(GatewaySystemPrompt), "messaging app") {
		t.Error("GatewaySystemPrompt should frame the reply as a chat message")
	}
}
