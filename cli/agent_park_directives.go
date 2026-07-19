/*
 * ChatCLI - Mid-park user directives
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * While an agent is parked the REPL stays in normal chat mode — plain
 * text keeps its default behavior. Directing the parked agent is an
 * explicit action: /park-note <msg> persists the text into the park
 * snapshot as a pending directive, and RunResumed injects it into the
 * agent's context right after the park result at wake-up. A one-time
 * hint per park surfaces the command when the user chats while a park
 * is waiting, covering the "I typed and the agent never saw it" gap
 * without changing what plain input does.
 */
package cli

import (
	"fmt"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// registerActivePark records a park created by this session as waiting.
// Called by handleAgentPark right after the snapshot + resume job exist.
func (cli *ChatCLI) registerActivePark(token, resumeAtDisplay string) {
	cli.activeParkMu.Lock()
	defer cli.activeParkMu.Unlock()
	for _, p := range cli.activeParks {
		if p.Token == token {
			return
		}
	}
	cli.activeParks = append(cli.activeParks, activePark{Token: token, ResumeAtDisplay: resumeAtDisplay})
}

// unregisterActivePark removes a park from the waiting set. Called when
// a resume consumes the token (regardless of outcome — the wait is over)
// and by /cancel-park.
func (cli *ChatCLI) unregisterActivePark(token string) {
	cli.activeParkMu.Lock()
	defer cli.activeParkMu.Unlock()
	kept := cli.activeParks[:0]
	for _, p := range cli.activeParks {
		if p.Token != token {
			kept = append(kept, p)
		}
	}
	cli.activeParks = kept
}

// latestActivePark returns the most recently registered waiting park.
func (cli *ChatCLI) latestActivePark() (activePark, bool) {
	cli.activeParkMu.Lock()
	defer cli.activeParkMu.Unlock()
	if len(cli.activeParks) == 0 {
		return activePark{}, false
	}
	return cli.activeParks[len(cli.activeParks)-1], true
}

// handleParkNoteCommand implements /park-note <msg>: persist the text as
// a directive for the most recently parked agent of this session.
func (cli *ChatCLI) handleParkNoteCommand(text string) {
	if text == "" {
		fmt.Println(colorize("  "+i18n.T("park.note.usage"), ColorYellow))
		return
	}
	p, ok := cli.latestActivePark()
	if !ok {
		fmt.Println(colorize("  "+i18n.T("park.note.none"), ColorYellow))
		return
	}
	if err := park.AppendDirective(p.Token, text); err != nil {
		// Snapshot gone (cancelled/pruned behind our back): drop the stale
		// registry entry and tell the user instead of failing silently.
		cli.unregisterActivePark(p.Token)
		if cli.logger != nil {
			cli.logger.Warn("park: directive persist failed",
				zap.String("token", p.Token), zap.Error(err))
		}
		fmt.Println(colorize("  ⚠ "+i18n.T("park.note.failed", err), ColorYellow))
		return
	}
	short := p.Token
	if len(short) > 8 {
		short = short[:8]
	}
	fmt.Println(colorize("  💬 "+i18n.T("park.directive.queued", p.ResumeAtDisplay, short), ColorCyan))
}

// maybeShowParkNoteHint prints a one-line reminder — once per park — when
// the user sends a plain chat message while an agent is parked, so the
// /park-note channel is discoverable exactly when it matters.
func (cli *ChatCLI) maybeShowParkNoteHint() {
	cli.activeParkMu.Lock()
	defer cli.activeParkMu.Unlock()
	if len(cli.activeParks) == 0 {
		return
	}
	last := &cli.activeParks[len(cli.activeParks)-1]
	if last.HintShown {
		return
	}
	last.HintShown = true
	short := last.Token
	if len(short) > 8 {
		short = short[:8]
	}
	fmt.Println(colorize("  ℹ "+i18n.T("park.note.hint", last.ResumeAtDisplay, short), ColorGray))
}
