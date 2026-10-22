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

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/scheduler"
)

// liveParkAdapter implements plugins.ParkCanceller over this process's park
// runtime: it drops the scheduled wake and delivers an immediate wake with
// outcome=cancelled through the same path the scheduler uses, so the parked
// agent resumes right away on whichever surface owns it (REPL queue or the
// in-turn waiter of an unattended surface). Output is English text for the
// LLM.
type liveParkAdapter struct {
	cli *ChatCLI
}

// newLiveParkAdapter builds the adapter bound to the CLI.
func newLiveParkAdapter(cli *ChatCLI) *liveParkAdapter { return &liveParkAdapter{cli: cli} }

// parkCancelOutcome is the outcome string the resumed agent sees.
const parkCancelOutcome = "cancelled"

// CancelPark implements plugins.ParkCanceller.
func (a *liveParkAdapter) CancelPark(token, reason string) (string, error) {
	if a == nil || a.cli == nil {
		return "", errors.New("park runtime not bound to the CLI")
	}
	snap, err := park.Load(token)
	if err != nil {
		return "", err
	}
	if a.cli.hasPendingResume(token) || a.cli.wasRecentlyResumed(token) {
		return "", fmt.Errorf("park %s is already resuming", token)
	}
	if a.cli.schedulerBridge == nil {
		return "", errors.New("park runtime not ready (scheduler bridge missing)")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "cancelled via @park cancel"
	}

	var notes []string
	if snap.SchedulerJobID != "" && a.cli.scheduler != nil {
		owner := scheduler.Owner{Kind: "park", ID: "agent", Tag: snap.Token}
		switch cerr := a.cli.scheduler.Cancel(scheduler.JobID(snap.SchedulerJobID), reason, owner); {
		case cerr == nil:
			notes = append(notes, "scheduler job "+snap.SchedulerJobID+" cancelled")
		case errors.Is(cerr, scheduler.ErrJobTerminal), errors.Is(cerr, scheduler.ErrJobNotFound):
			notes = append(notes, "scheduler job "+snap.SchedulerJobID+" was already finished")
		default:
			return "", fmt.Errorf("cancel scheduler job %s: %w", snap.SchedulerJobID, cerr)
		}
	}
	if err := a.cli.schedulerBridge.wakeParkedAgent(token, parkCancelOutcome, reason); err != nil {
		return "", err
	}
	if a.cli.unattended {
		notes = append(notes, "wake delivered to the in-turn waiter")
	} else {
		notes = append(notes, "resume queued; it runs as soon as the REPL is idle")
	}
	return strings.Join(notes, "; "), nil
}
