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
	fmt.Fprintf(&sb, "pruned %d parked snapshot(s) proven finished (%s).", len(rep.removed), scope)
	if len(rep.live) > 0 {
		fmt.Fprintf(&sb, "\nkept %d park(s) still waiting, which prune never removes: %s. To end one on purpose use {\"cmd\":\"discard\",\"args\":{\"token\":\"<prefix>\"}}.",
			len(rep.live), strings.Join(parkShorts(rep.live), ", "))
	}
	if len(rep.unknown) > 0 {
		fmt.Fprintf(&sb, "\nkept %d park(s) whose state is unknown here (no scheduler in this process knows their job and no owner answered — they may be waiting in another process, or were created moments ago): %s. Prune never removes these; only the user can, by age, with /parked gc <age>.",
			len(rep.unknown), strings.Join(parkShorts(rep.unknown), ", "))
	}
	if len(rep.retry) > 0 {
		shorts := parkShorts(rep.retry)
		fmt.Fprintf(&sb, "\nkept %d park(s) whose wait is over but whose agent did not continue — its resume failed or was interrupted, or its owner died before resuming it; prune never removes these: %s. The user retries one with /resume %s; to end one on purpose use discard.",
			len(shorts), strings.Join(shorts, ", "), shorts[0])
	}
	for _, e := range rep.errs {
		fmt.Fprintf(&sb, "\ncould not delete %v", e)
	}
	return sb.String(), nil
}

// NotePark implements plugins.ParkNoter: the tool's note, under the rules
// /park-note --token follows. A note for a park that will not read it, or
// that another live process owns, is refused instead of written.
func (a *liveParkAdapter) NotePark(token, text string) error {
	if a == nil || a.cli == nil {
		return errors.New("park runtime not bound to the CLI")
	}
	snap, err := park.Load(token)
	if err != nil {
		return err
	}
	switch st := a.cli.parkStatusOf(snap); {
	case st.resumed:
		return fmt.Errorf("park %s already resumed (or is resuming now): nobody would read the note; nothing was recorded", parkShort(token))
	case st.verdict == parkFinished:
		return fmt.Errorf("park %s is finished (its resume completed): nobody would read the note; nothing was recorded", parkShort(token))
	case st.heldBy != "":
		return fmt.Errorf("park %s is owned by another live process (%s), which rewrites its snapshot without a lock this process shares; the note must be sent from there. Nothing was recorded", parkShort(token), st.heldBy)
	}
	return park.AppendDirective(token, text)
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
	// The same notion the sweep uses: queued, taken by a drain, running or
	// just resumed. A token the drain already took is in neither the queue
	// nor the recent set, and waking it queued a second resume.
	if a.cli.isResuming(token) {
		return "", fmt.Errorf("park %s is already resuming", token)
	}
	reason = strings.TrimSpace(reason)

	owned := a.cli.ownsPark(token)
	if !owned {
		if owner, alive := remoteParkLiveOwner(token); alive {
			// Its owner drops a wake for a park it is already resuming:
			// say that, instead of promising a resume with this outcome.
			if pending, fired := a.cli.parkJobIndex().of(snap); snap.ResumeState == park.ResumeRunning || (fired && !pending) {
				return "", fmt.Errorf("park %s is already resuming in the process that owns it (%s): its wake already fired and a resume is under way there; nothing was changed", token, describeParkOwner(owner))
			}
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
