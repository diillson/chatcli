/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// The web page's controls: switching MCP servers and pinned skills on and
// off, and completing a slash command's arguments. Each goes through the
// same code the terminal command uses (/mcp start|stop, /skill pin|unpin,
// the REPL completer), so the page and the terminal cannot drift apart.
// Like their terminal commands, both switches last for the running
// process: MCP servers come back as configured on the next start, and
// pinned skills are per session.

// SetMCPServerRunningRPC starts (on) or stops (off) one configured MCP
// server, as /mcp start and /mcp stop do.
func (cli *ChatCLI) SetMCPServerRunningRPC(name string, on bool) error {
	if cli.mcpManager == nil {
		return errors.New(i18n.T("mcp.cmd.not_enabled"))
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New(i18n.T("web.controls.mcp_name_required"))
	}
	if err := cli.mcpSetRunning(name, on); err != nil {
		key := "mcp.cmd.stop_error"
		if on {
			key = "mcp.cmd.start_error"
		}
		return errors.New(i18n.T(key, translateMCPError(err)))
	}
	// The MCP tool catalog is part of the stable prefix, as handleMCPCommand
	// notes for the terminal command.
	if on {
		cli.notePrefixChanged("mcp start")
	} else {
		cli.notePrefixChanged("mcp stop")
	}
	return nil
}

// WebSkillStateRPC reports which skills are pinned for this process's
// session and which are manual-only (disable-model-invocation), which
// pinning refuses.
func (cli *ChatCLI) WebSkillStateRPC() (pinned, manualOnly []string) {
	if cli.skillHandler == nil {
		return nil, nil
	}
	pinned = cli.skillHandler.PinnedNames()
	if cli.skillHandler.personaMgr == nil {
		return pinned, nil
	}
	skills, err := cli.skillHandler.personaMgr.ListSkills()
	if err != nil {
		return pinned, nil
	}
	for _, s := range skills {
		if s != nil && s.DisableModelInvocation {
			manualOnly = append(manualOnly, s.Name)
		}
	}
	return pinned, manualOnly
}

// SetSkillPinnedRPC pins (on) or unpins (off) a skill, as /skill pin and
// /skill unpin do.
func (cli *ChatCLI) SetSkillPinnedRPC(name string, on bool) error {
	if cli.skillHandler == nil {
		return errors.New(i18n.T("skill.pin.no_manager"))
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New(i18n.T("web.controls.skill_name_required"))
	}
	if on {
		skill, _, err := cli.skillHandler.pinSkill(name)
		switch {
		case errors.Is(err, errSkillPinNoManager):
			return errors.New(i18n.T("skill.pin.no_manager"))
		case errors.Is(err, errSkillPinNotFound):
			return fmt.Errorf("%s: %s", i18n.T("skill.pin.not_found"), name)
		case errors.Is(err, errSkillPinManualOnly):
			return fmt.Errorf("%s: %s", i18n.T("skill.pin.disabled_invocation"), skill.Name)
		}
	} else {
		cli.skillHandler.unpinSkill(name)
	}
	// Pinned skills live in the stable prefix, as handleSkillCommand notes.
	if on {
		cli.notePrefixChanged("skill pin")
	} else {
		cli.notePrefixChanged("skill unpin")
	}
	cli.markGraphDirty()
	return nil
}

// CompletionRPC is one completion the page offers for the word being typed.
type CompletionRPC struct {
	Text        string `json:"text"`
	Description string `json:"description,omitempty"`
}

// webCompletionMax bounds one completion list: the page shows a short
// scrolling list under the cursor, not a catalog.
const webCompletionMax = 50

// WebCompleteRPC completes the last word of a slash command line from the
// REPL's own completer, for the commands the page can run. The page
// replaces the word after the last space with the chosen text, as the
// terminal prompt does. A line that names a command the page does not run
// gets no completions.
func (cli *ChatCLI) WebCompleteRPC(line string) (out []CompletionRPC) {
	if !strings.HasPrefix(line, "/") {
		return nil
	}
	root := line
	if i := strings.IndexAny(line, " \t"); i >= 0 {
		root = line[:i]
	} else {
		// The root word itself: the page completes it from its command
		// list, which is exactly what it can run.
		return nil
	}
	if !cli.webCommandAllowed(strings.TrimPrefix(root, "/")) {
		return nil
	}
	defer func() {
		// A suggester that panics on a partial line must not take the
		// request down; the page simply shows no completions.
		if rec := recover(); rec != nil {
			out = nil
			if cli.logger != nil {
				cli.logger.Debug("web completion suggester panicked", zap.String("command", root), zap.Any("panic", rec))
			}
		}
	}()
	for _, s := range cli.paletteSuggest(line) {
		t := strings.TrimSpace(s.Text)
		if t == "" {
			continue
		}
		out = append(out, CompletionRPC{Text: t, Description: s.Desc})
		if len(out) == webCompletionMax {
			break
		}
	}
	return out
}

// webCommandAllowed reports whether the page runs the named command (no
// leading slash): the commands it advertises, the mode switches included.
func (cli *ChatCLI) webCommandAllowed(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range cli.ListWebCommands() {
		if c.Name == name {
			return true
		}
	}
	return false
}
