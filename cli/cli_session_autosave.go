/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ChatCLI - cli_session_autosave.go
 *
 * Auto-saves the live conversation when the interactive REPL exits, so a
 * session is never lost just because the user didn't run /session save.
 * Combined with ranked session search and @session get, this closes the
 * recall loop: every conversation becomes retrievable memory by default.
 *
 * Autosaves live under a reserved "autosave-" name prefix — they never touch
 * user-named sessions — and are pruned to a small keep-count so /session list
 * stays readable and disk stays bounded (the 90-day session TTL applies too).
 * Gated by CHATCLI_SESSION_AUTOSAVE (default on).
 */
package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

const (
	// autosavePrefix reserves the namespace; user session names that happen
	// to start with it are indistinguishable by design (docs call it out).
	autosavePrefix = "autosave-"

	// autosaveKeep bounds how many autosaves survive pruning.
	autosaveKeep = 10

	// autosaveMinMessages skips trivial sessions: one prompt and its answer
	// are worth keeping, a lone /command or an empty boot is not.
	autosaveMinMessages = 2
)

// sessionAutosaveEnabled reads CHATCLI_SESSION_AUTOSAVE; unset means enabled.
func sessionAutosaveEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_SESSION_AUTOSAVE"))) {
	case "false", "0", "off", "no", "disabled":
		return false
	}
	return true
}

// autosaveSessionOnExit persists the live conversation under a timestamped
// autosave name, then prunes old autosaves. Runs once per process (cleanup
// can be reached from both the normal defer and the /exit fast path).
func (cli *ChatCLI) autosaveSessionOnExit() {
	if cli.sessionAutosaved || !sessionAutosaveEnabled() {
		return
	}
	cli.sessionAutosaved = true

	if cli.sessionManager == nil {
		return
	}
	nonSystem := 0
	for _, m := range cli.history {
		if m.Role != "system" {
			nonSystem++
		}
	}
	if nonSystem < autosaveMinMessages {
		return
	}

	name := autosavePrefix + time.Now().Format("20060102-150405")
	if err := cli.sessionManager.SaveSessionV2(name, cli.buildSessionData()); err != nil {
		cli.logger.Warn("session autosave failed", zap.Error(err))
		return
	}
	fmt.Println(colorize("  "+i18n.T("session.autosave.saved", name), ColorGray))

	cli.pruneAutosaves()
}

// pruneAutosaves bounds the REPL autosave set to the newest autosaveKeep
// files, via the shared machine-session pruner.
func (cli *ChatCLI) pruneAutosaves() {
	cli.sessionManager.PruneSessionsByPrefix(autosavePrefix, autosaveKeep)
}
