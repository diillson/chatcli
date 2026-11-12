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

	"github.com/diillson/chatcli/cli/hooks"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// /switch dropped what it did not read: a provider name or a mistyped flag
// did nothing and said nothing.
func TestSwitchReportsWhatItDoesNotRead(t *testing.T) {
	c := &ChatCLI{}
	for _, line := range []string{"/switch openai", "/switch --modelo gpt-x", "/switch --model gpt-x extra"} {
		arg := strings.Fields(line)[1]
		if strings.HasPrefix(line, "/switch --model ") {
			arg = "extra"
		}
		out := captureStdout(t, func() { c.handleSwitchCommand(context.Background(), line) })
		if !strings.Contains(out, i18n.T("cli.switch.unknown_arg", arg)) {
			t.Errorf("%s was not reported:\n%s", line, out)
		}
	}
	for _, flag := range []string{"--max-tokens", "--realm", "--agent-id"} {
		if got := switchFlagMissingValue([]string{"/switch", flag}); got != flag {
			t.Errorf("%s with no value: %q", flag, got)
		}
	}
	if got := switchFlagMissingValue([]string{"/switch", "--model"}); got != "" {
		t.Errorf("bare --model lists the models; reported %q", got)
	}
	if got := switchFlagMissingValue([]string{"/switch", "--max-tokens", "100"}); got != "" {
		t.Errorf("a flag with its value was reported: %q", got)
	}
}

// Completion always offered "/hooks reload"; the handler never read it.
func TestHooksReloadIsACommand(t *testing.T) {
	c := &ChatCLI{hookManager: hooks.NewManager(zap.NewNop())}
	out := captureStdout(t, func() { c.handleHooksCommand("/hooks reload") })
	if !strings.Contains(out, i18n.T("hooks.cmd.reloaded", len(c.hookManager.GetHooks()))) {
		t.Fatalf("/hooks reload did not reload:\n%s", out)
	}
	out = captureStdout(t, func() { c.handleHooksCommand("/hooks relaod") })
	if !strings.Contains(out, i18n.T("hooks.cmd.unknown_sub", "relaod")) {
		t.Fatalf("a mistyped subcommand was not reported:\n%s", out)
	}
	out = captureStdout(t, func() { c.handleHooksCommand("/hooks list") })
	if strings.Contains(out, "list\"") || strings.Contains(out, i18n.T("hooks.cmd.reloaded", 0)) {
		t.Fatalf("/hooks list misbehaved:\n%s", out)
	}
}
