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

	"github.com/diillson/chatcli/cli/agent"
	"go.uber.org/zap"
)

// i18n is initialized by TestMain in config_sections_test.go.

func TestDangerBlocked_PolicyMatrix(t *testing.T) {
	logger := zap.NewNop()
	cases := []struct {
		name        string
		unattended  bool
		dangerBlock bool
		want        bool
	}{
		{"attended sem policy", false, false, false},
		{"attended com policy", false, true, false},
		{"unattended sem policy (gateway)", true, false, false},
		{"unattended com policy (mcp block)", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &ChatCLI{unattended: tc.unattended, dangerBlock: tc.dangerBlock}
			a := NewAgentMode(c, logger)
			if got := a.dangerBlocked(); got != tc.want {
				t.Fatalf("dangerBlocked() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSetRPCDangerPolicy(t *testing.T) {
	c := &ChatCLI{unattended: true}
	c.SetRPCDangerPolicy(true)
	a := NewAgentMode(c, zap.NewNop())
	if !a.dangerBlocked() {
		t.Fatal("SetRPCDangerPolicy(true) should arm the unattended danger gate")
	}
	c.SetRPCDangerPolicy(false)
	if a.dangerBlocked() {
		t.Fatal("SetRPCDangerPolicy(false) should disarm the unattended danger gate")
	}
}

// TestRunSingleCommand_DangerBlockedDeclinesInBand proves that with the block
// policy armed a dangerous command is declined in the transcript (the model
// can replan) instead of auto-approved — and, critically, without any stdin
// read: os.Stdin carries the JSON-RPC protocol in the MCP server.
func TestRunSingleCommand_DangerBlockedDeclinesInBand(t *testing.T) {
	logger := zap.NewNop()
	c := &ChatCLI{unattended: true, dangerBlock: true}
	a := NewAgentMode(c, logger)

	renderer := agent.NewUIRenderer(logger)
	var out strings.Builder
	var lastErr string

	dangerous := "rm -rf /"
	if !a.validator.IsDangerous(dangerous) {
		t.Fatalf("fixture %q should be classified dangerous", dangerous)
	}

	block := agent.CommandBlock{Commands: []string{dangerous}}
	a.runSingleCommand(context.Background(), block, renderer, 0, dangerous, &out, &lastErr)

	if out.Len() == 0 {
		t.Fatal("expected an in-band decline message in the transcript")
	}
	if strings.Contains(out.String(), "exit=") {
		t.Fatalf("dangerous command must not execute; transcript: %q", out.String())
	}
}
