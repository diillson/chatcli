/*
 * ChatCLI - Mid-park user directives
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * While an agent is parked, the REPL is back in chat mode — but the
 * user's plain text is almost always meant for the parked agent ("be
 * more detailed next report"), not for a tool-less chat turn over the
 * same history. This file owns the session-local registry of waiting
 * parks and the capture hook the executor calls for plain input: the
 * text is persisted into the park snapshot as a pending directive and
 * echoed back with the resume ETA, and RunResumed injects it into the
 * agent's context right after the park result.
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

// captureParkDirective intercepts plain input while a park is waiting.
// Returns true when the text was captured (the executor must NOT route
// it to chat). On persistence failure it returns false so the input
// degrades to the old chat behavior instead of being lost.
func (cli *ChatCLI) captureParkDirective(in string) bool {
	p, ok := cli.latestActivePark()
	if !ok {
		return false
	}
	if err := park.AppendDirective(p.Token, in); err != nil {
		// Snapshot gone (cancelled/pruned behind our back): drop the stale
		// registry entry and let the input flow to chat.
		cli.unregisterActivePark(p.Token)
		if cli.logger != nil {
			cli.logger.Warn("park: directive capture failed; falling back to chat",
				zap.String("token", p.Token), zap.Error(err))
		}
		return false
	}
	short := p.Token
	if len(short) > 8 {
		short = short[:8]
	}
	fmt.Println(colorize("  💬 "+i18n.T("park.directive.queued", p.ResumeAtDisplay, short), ColorCyan))
	return true
}
