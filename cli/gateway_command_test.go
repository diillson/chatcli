/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strings"
	"testing"
)

func TestGatewayCleanLine(t *testing.T) {
	cases := map[string]string{
		"  hello  ": "hello",
		"":          "",
		"   ":       "",
		"┌──────────────┐":    "", // pure box-drawing -> dropped
		"│ Step 1: read │":    "│ Step 1: read │",
		"●●●":                 "", // spinner glyphs -> dropped
		"running go build...": "running go build...",
	}
	for in, want := range cases {
		if got := gatewayCleanLine(in); got != want {
			t.Errorf("gatewayCleanLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGatewaySessions(t *testing.T) {
	s := newGatewaySessions(2)
	if s.preamble("a") != "" {
		t.Error("new session should have empty preamble")
	}
	s.remember("a", "first")
	s.remember("a", "second")
	s.remember("a", "third") // evicts "first" (cap 2)

	pre := s.preamble("a")
	if strings.Contains(pre, "first") {
		t.Errorf("oldest request should be evicted: %q", pre)
	}
	if !strings.Contains(pre, "second") || !strings.Contains(pre, "third") {
		t.Errorf("preamble missing recent requests: %q", pre)
	}
	// Blank input is ignored, and unrelated sessions stay isolated.
	s.remember("a", "   ")
	if strings.Count(s.preamble("a"), "\n- ") != 2 {
		t.Errorf("blank request should not be stored: %q", s.preamble("a"))
	}
	if s.preamble("b") != "" {
		t.Error("sessions must be isolated")
	}
}

func TestGatewayAgentFunc_NoClient(t *testing.T) {
	c := &ChatCLI{} // Client is nil
	fn := c.gatewayAgentFunc(newGatewaySessions(5))
	if _, err := fn(context.Background(), "s", "hi"); err == nil {
		t.Error("expected error when no active model")
	}
}
