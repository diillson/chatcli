/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * acp_support.go
 *
 * Slash-command surface for the ACP server: the allowlisted subset of the
 * REPL's commands that works headless (no TUI, no stdin prompt), announced
 * to ACP clients via available_commands_update and executed through the
 * regular CommandHandler with stdout captured (scheduler_bridge pattern).
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/diillson/chatcli/cli/palette"
	"github.com/diillson/chatcli/i18n"
)

// ACPCommandInfo describes one slash command advertised to ACP clients.
type ACPCommandInfo struct {
	Name        string // without the leading slash (ACP convention)
	Description string
	InputHint   string
}

// acpCommandAllow is the explicit allowlist of REPL commands runnable
// headless over ACP. Everything else is refused with a clear message.
// Excluded on purpose:
//   - TUI/lifecycle: /menu (interactive menu), /exit /quit (kill the server),
//     /clear /reload (terminal/session lifecycle), /rewind /retry /retryall
//     /nextchunk /skipchunk (REPL chunk flow), /update (self-update),
//     /auth (browser/stdin login flow), /plan /worktree (interactive UIs).
//   - Daemons/channels: /gateway /connect /disconnect /channel /hub /watch
//     (long-lived processes bound to the operator terminal).
//   - Blocking/stdin: /hooks /lsp (management UIs), /schedule /wait /jobs
//     /parked /resume /cancel-park (scheduler owns the interactive side).
//
// /agent, /coder and /run are NOT here: the ACP prompt handler drains them
// first as session mode switches (they panic-request mode in the REPL).
var acpCommandAllow = map[string]bool{
	"/switch": true, "/provider": true, "/model": true, "/max-tokens": true,
	"/config": true, "/session": true, "/newsession": true, "/compact": true,
	"/export": true, "/context": true, "/memory": true,
	"/thinking": true, "/refine": true, "/verify": true, "/reflect": true, "/moa": true,
	"/mcp": true, "/websearch": true, "/skill": true, "/plugin": true,
	"/cost": true, "/metrics": true, "/ratelimit": true,
	"/version": true, "/help": true, "/agents": true, "/board": true,
}

// acpModeCommands are advertised alongside the allowlist but handled by the
// ACP layer itself as session mode switches, never dispatched to the REPL
// CommandHandler. Built per call — i18n resolves at runtime, not init.
func acpModeCommands() []ACPCommandInfo {
	return []ACPCommandInfo{
		{Name: "coder", Description: i18n.T("acp.command.coder_desc"), InputHint: i18n.T("acp.command.task_hint")},
		{Name: "chat", Description: i18n.T("acp.command.chat_desc")},
		{Name: "agent", Description: i18n.T("acp.command.agent_desc"), InputHint: i18n.T("acp.command.task_hint")},
	}
}

// ListACPCommands returns the commands advertised to ACP clients: the three
// mode switches plus the headless allowlist, with the palette registry's
// localized summaries.
func (cli *ChatCLI) ListACPCommands() []ACPCommandInfo {
	modes := acpModeCommands()
	out := make([]ACPCommandInfo, 0, len(acpCommandAllow)+len(modes))
	out = append(out, modes...)
	for _, rc := range palette.RootCommands() {
		if !acpCommandAllow[rc.Name] {
			continue
		}
		out = append(out, ACPCommandInfo{
			Name:        rc.Name[1:], // ACP command names carry no slash
			Description: rc.Summary(),
			InputHint:   i18n.T("acp.command.args_hint"),
		})
	}
	return out
}

// IsACPCommandAllowed reports whether a slash token (with leading slash) is
// runnable headless over ACP.
func (cli *ChatCLI) IsACPCommandAllowed(token string) bool {
	return acpCommandAllow[token]
}

// RunSlashCommandRPC executes one allowlisted slash command headless,
// capturing its stdout as the result. Mirrors the scheduler bridge's
// HandleCommand invocation: panic sentinels are flow-control, not crashes.
// Caller must have validated the command against the allowlist — mode
// switches (/agent, /coder, /run) never reach here.
func (cli *ChatCLI) RunSlashCommandRPC(ctx context.Context, line string) (string, error) {
	// Bare "/switch" opens the REPL's numbered provider picker and parks
	// interactionState waiting for input that headless callers can never
	// send — the /provider listing is the same information, print-only.
	if strings.TrimSpace(line) == "/switch" {
		line = "/provider"
	}

	// captureRPCStdout holds rpcStdoutMu, so a command can never interleave
	// with an in-flight captured agent/coder run.
	var panicErr error
	out, err := captureRPCStdout(func() error {
		// Stdin quarantine: on the ACP/MCP server, os.Stdin IS the JSON-RPC
		// channel. Any handler that prompts for confirmation would fight the
		// protocol scanner for bytes and block forever holding rpcStdoutMu —
		// wedging every session. Swapping in /dev/null makes every stdin
		// read return EOF instantly, and all confirm sites treat EOF as
		// "no" (fail-safe deny). Serialized by rpcStdoutMu like the stdout
		// swap; the REPL is never inside a captured run.
		if devnull, derr := os.Open(os.DevNull); derr == nil {
			origStdin := os.Stdin
			os.Stdin = devnull
			defer func() {
				os.Stdin = origStdin
				_ = devnull.Close()
			}()
		}
		// A handler that parks interaction state (e.g. a picker) leaves the
		// REPL-only state machine dangling headless — restore it on exit.
		prevState := cli.interactionState
		defer func() { cli.interactionState = prevState }()

		defer func() {
			if rec := recover(); rec != nil {
				recErr, _ := rec.(error)
				switch {
				case errors.Is(recErr, errExitRequest):
					panicErr = fmt.Errorf("%s", i18n.T("acp.command_unsupported", line))
				case errors.Is(recErr, errAgentModeRequest), errors.Is(recErr, errCoderModeRequest):
					// The ACP prompt handler drains /agent|/coder|/run before
					// dispatching here; reaching this branch means another
					// command requested a mode switch — surface it.
					panicErr = fmt.Errorf("acp: slash command %q unexpectedly requested an agent/coder mode switch", line)
				default:
					panicErr = fmt.Errorf("acp: slash command panic: %v", rec)
				}
			}
		}()
		if cli.commandHandler == nil {
			panicErr = fmt.Errorf("acp: commandHandler not initialized")
			return nil
		}
		if cli.commandHandler.HandleCommand(ctx, line) {
			panicErr = fmt.Errorf("%s", i18n.T("acp.command_unsupported", line))
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	if panicErr != nil {
		return out, panicErr
	}
	if strings.TrimSpace(out) == "" {
		out = i18n.T("acp.command_empty_output")
	}
	return out, nil
}
