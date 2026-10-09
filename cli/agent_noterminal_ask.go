/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
	"golang.org/x/term"
)

// stdinIsInteractive reports whether a human can answer a confirmation
// prompt on stdin. A test seam: production reads the real terminal state.
var stdinIsInteractive = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// noTerminalAskBlocked resolves a policy "ask" when nobody can answer it: a
// one-shot (`chatcli -p "/coder …"` from a script, CI or `chatcli eval`)
// whose stdin is a pipe, a file or /dev/null. The security prompt would
// otherwise wait on a stdin that already hit EOF until the run's timeout —
// the whole run hung on an unanswerable question — so the action is denied
// at once, fail-safe, and the model is told why so it can continue without
// it. Unattended surfaces (gateway, ACP, MCP) never get here: they resolve
// asks through their own permission channel first. Returns true when the
// action was blocked.
func (a *AgentMode) noTerminalAskBlocked(toolName, rawArgs string, renderError func(string)) bool {
	if stdinIsInteractive() {
		return false
	}
	title := agent.CompactToolLabel(extractSubcmdFromArgs(rawArgs), rawArgs)
	if strings.TrimSpace(title) == "" {
		title = toolName
	}
	if a.logger != nil {
		a.logger.Info("coder policy: 'ask' denied (no terminal to confirm)", zap.String("tool", toolName))
	}
	msg := i18n.T("coder.security.no_terminal_denied", title)
	renderError(msg)
	a.emitBlockedTool(toolName, rawArgs, msg)
	a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: fmt.Sprintf(
		"SECURITY BLOCK: %q needs the user's confirmation, but this run has no interactive terminal to ask (stdin is not a TTY). The action was NOT executed and the user did not deny it. DO NOT retry it; continue without it, and in your final answer say which action needs approval: the user can allow it in coder_policy.json or rerun interactively.",
		title)})
	return true
}

// workerNoTerminalDenial is noTerminalAskBlocked for parallel workers: the
// worker gets the refusal as its tool result.
func workerNoTerminalDenial(toolName string) (bool, string) {
	if stdinIsInteractive() {
		return false, ""
	}
	return true, i18n.T("coder.security.no_terminal_denied", toolName)
}
