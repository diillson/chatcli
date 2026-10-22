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
)

// liveParkAdapter implements plugins.ParkCanceller over the park runtime.
// A park this process owns is woken here: the scheduled wake is dropped and
// an immediate wake with outcome=cancelled goes through the same path the
// scheduler uses (REPL queue or the in-turn waiter of an unattended
// surface). A park another live process owns is cancelled there: the wake
// request travels through the Conversation Hub (park_hub_bridge) and the
// owner — the only process holding the park's scheduler job and wake path —
// applies it on its next tick. A park whose owner is gone is resumed here.
// Output is English text for the LLM.
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
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "cancelled via @park cancel"
	}

	owned := a.cli.ownsPark(token)
	if !owned {
		if owner, alive := remoteParkLiveOwner(token); alive {
			if !requestRemoteParkWake(token, parkCancelOutcome, reason) {
				return "", fmt.Errorf("park %s is owned by %s but the wake request could not be recorded in the hub", token, describeParkOwner(owner))
			}
			return fmt.Sprintf("cancel delivered via the hub to the process that owns this park (%s); it drops the scheduler job and resumes the agent there with outcome=cancelled on its next sync tick",
				describeParkOwner(owner)), nil
		}
	}

	if a.cli.schedulerBridge == nil {
		return "", errors.New("park runtime not ready (scheduler bridge missing)")
	}
	var notes []string
	note, err := a.cli.cancelOwnParkJob(snap, reason)
	if err != nil {
		return "", fmt.Errorf("cancel scheduler job %s: %w", snap.SchedulerJobID, err)
	}
	if note != "" {
		notes = append(notes, note)
	}
	if !owned {
		notes = append(notes, "no live process owns this park (its session ended); resuming it here")
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
