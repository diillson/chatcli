/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/palette"
)

// paletteModeSwitch lists commands that enter a long-running mode; they must
// never be intercepted by the per-command palette, since the mode-switch
// unwind handles them instead.
var paletteModeSwitch = map[string]bool{
	"/agent": true, "/run": true, "/coder": true, "/plan": true,
}

// paletteSuggest returns the next-token suggestions for a command line by
// running the live inline completer against a synthesized document. The
// palette and the prompt therefore share one source of truth for every
// command's subcommands, flags and dynamic values (models, sessions, config
// sections, files, …) — nothing is duplicated or hand-maintained.
func (cli *ChatCLI) paletteSuggest(line string) []palette.Suggestion {
	buf := prompt.NewBuffer()
	buf.InsertText(line, false, true)
	raw := cli.completer(*buf.Document())
	out := make([]palette.Suggestion, 0, len(raw))
	for _, s := range raw {
		out = append(out, palette.Suggestion{Text: s.Text, Desc: s.Description})
	}
	return out
}

// paletteTrigger decides whether a submitted line should open the command
// palette, returning the command to scope to ("" for the root listing) and
// whether to trigger at all. It fires for the explicit root aliases and for
// any bare, pickable command.
func (cli *ChatCLI) paletteTrigger(userInput string) (target string, ok bool) {
	// Only the interactive REPL can host the overlay; headless callers
	// (scheduler, gateway, one-shot) must run the command as typed.
	if !cli.replActive {
		return "", false
	}
	// A bare command the palette just prefilled (e.g. "/switch", "/config")
	// must run its own action this once, not reopen the overlay.
	if cli.suppressPaletteOnce {
		cli.suppressPaletteOnce = false
		return "", false
	}
	s := strings.TrimSpace(userInput)
	switch s {
	case "/", "/menu", "/commands", "/palette":
		return "", true
	}
	// A bare, single-token slash command (no arguments yet) that offers
	// concrete options opens scoped to itself, e.g. "/model" → model list.
	if strings.HasPrefix(s, "/") && !strings.ContainsAny(s, " \t") {
		if cli.commandIsPickable(s) {
			return s, true
		}
	}
	return "", false
}

// commandIsPickable reports whether a bare command should open the per-command
// palette: not a mode switch, and offering at least one concrete next-token
// option (subcommand, flag or value).
func (cli *ChatCLI) commandIsPickable(cmd string) bool {
	if paletteModeSwitch[cmd] {
		return false
	}
	return palette.HasConcreteOption(cli.paletteSuggest(cmd + " "))
}
