/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

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

// parkManualOutcome is the outcome /resume and @park resume report.
const parkManualOutcome = "manual"

// CancelPark implements plugins.ParkCanceller.
func (a *liveParkAdapter) CancelPark(token, reason string) (string, error) {
	if strings.TrimSpace(reason) == "" {
		reason = "cancelled via @park cancel"
	}
	return a.wakePark(token, parkCancelOutcome, reason)
}

// ResumePark implements plugins.ParkManager: the wake /resume performs,
// through the same cross-process path as CancelPark.
func (a *liveParkAdapter) ResumePark(token string) (string, error) {
	return a.wakePark(token, parkManualOutcome, "resumed via @park resume")
}

// DiscardPark implements plugins.ParkManager: what /cancel-park does. A park
// another live process is known to own is refused — its scheduler job lives
// there and would fire on nothing. An owner that is not mirrored or stopped
// heartbeating is not seen, and that park is discarded.
func (a *liveParkAdapter) DiscardPark(token string) (string, error) {
	if a == nil || a.cli == nil {
		return "", errors.New("park runtime not bound to the CLI")
	}
	if _, err := park.Load(token); err != nil {
		return "", err
	}
	if !a.cli.ownsPark(token) {
		if owner, alive := remoteParkLiveOwner(token); alive {
			return "", fmt.Errorf("park %s is owned by another live process (%s); discard it there with /cancel-park, or use cancel to wake it", token, describeParkOwner(owner))
		}
	}
	jobErr, err := a.cli.discardPark(token, "discarded via @park discard")
	if err != nil {
		return "", err
	}
	out := "snapshot deleted; this agent will not resume"
	if jobErr != nil {
		out += fmt.Sprintf(" (its scheduler job could not be cancelled: %v)", jobErr)
	}
	return out, nil
}

// PrunePark implements plugins.ParkPruner over the sweep /parked prune runs.
// Waiting parks are named in the result: the model asked for a cleanup and
// must learn that those were left alone on purpose, not missed.
func (a *liveParkAdapter) PrunePark(olderThan string) (string, error) {
	if a == nil || a.cli == nil {
		return "", errors.New("park runtime not bound to the CLI")
	}
	var age time.Duration
	scope := "all snapshots"
	if strings.TrimSpace(olderThan) != "" {
		d, err := parseParkAge(olderThan)
		if err != nil {
			return "", fmt.Errorf("older_than %q is not a positive age (use a duration such as 24h, or whole days such as 7d)", olderThan)
		}
		age, scope = d, "snapshots older than "+d.String()
	}
	rep := a.cli.sweepDeadParks(age)
	var sb strings.Builder
	fmt.Fprintf(&sb, "pruned %d parked snapshot(s) nobody was waiting on (%s).", len(rep.removed), scope)
	if len(rep.live) > 0 {
		short := make([]string, 0, len(rep.live))
		for _, token := range rep.live {
			short = append(short, parkShort(token))
		}
		sort.Strings(short)
		fmt.Fprintf(&sb, "\nkept %d park(s) still waiting, which prune never removes: %s. To end one on purpose use {\"cmd\":\"discard\",\"args\":{\"token\":\"<prefix>\"}}.",
			len(short), strings.Join(short, ", "))
	}
	for _, e := range rep.errs {
		fmt.Fprintf(&sb, "\ncould not delete %v", e)
	}
	return sb.String(), nil
}

// wakePark resumes a parked agent now with the given outcome, here or in
// the live process that owns it.
func (a *liveParkAdapter) wakePark(token, outcome, reason string) (string, error) {
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

	owned := a.cli.ownsPark(token)
	if !owned {
		if owner, alive := remoteParkLiveOwner(token); alive {
			if !requestRemoteParkWake(token, outcome, reason) {
				return "", fmt.Errorf("park %s is owned by %s but the wake request could not be recorded in the hub", token, describeParkOwner(owner))
			}
			return fmt.Sprintf("wake delivered via the hub to the process that owns this park (%s); it drops the scheduler job and resumes the agent there with outcome=%s on its next sync tick",
				describeParkOwner(owner), outcome), nil
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
	if err := a.cli.schedulerBridge.wakeParkedAgent(token, outcome, reason); err != nil {
		return "", err
	}
	if a.cli.unattended {
		notes = append(notes, "wake delivered to the in-turn waiter")
	} else {
		notes = append(notes, "resume queued; it runs as soon as the REPL is idle")
	}
	return strings.Join(notes, "; "), nil
}
