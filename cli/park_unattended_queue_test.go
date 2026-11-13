/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent/park"
)

// A wake that finds no in-turn waiter is queued. The terminal drains that
// queue at its next prompt, so there the park is still its own. A gateway,
// ACP or MCP process never drains it: a token it can never resume is not
// owned — it must not be announced in the hub, held against a sweep, or
// read as "already resuming" — and the park is what it is: its job fired
// and nobody resumed it.
func TestOnlyAProcessThatDrainsItsQueueOwnsAQueuedResume(t *testing.T) {
	for name, unattended := range map[string]bool{"the terminal": false, "an unattended surface": true} {
		t.Run(name, func(t *testing.T) {
			cli := resumeCLI(t, nil)
			cli.unattended = unattended
			cli.isExecuting.Store(true) // mid-turn: a wake is queued, nothing is typed into the TTY
			token := saveParkOwnedBy(t, os.Getpid(), enqueueParkJob(t, cli, true))
			_ = captureStdout(t, func() {
				if err := cli.schedulerBridge.wakeParkedAgent(token, "elapsed", ""); err != nil {
					t.Fatal(err)
				}
			})
			if !cli.hasPendingResume(token) {
				t.Fatal("the test needs the wake queued")
			}
			announced := strings.Contains(strings.Join(cli.ownedParkTokens(), ","), token)
			rep := cli.sweepDeadParks(0)
			if !unattended {
				if !announced || !cli.holdsPark(token) || !cli.isResuming(token) || len(rep.live) != 1 {
					t.Fatalf("the terminal drains its queue: the park is its own (announced=%v, sweep %+v)", announced, rep)
				}
				return
			}
			if announced {
				t.Error("a process that never drains its queue announced the park as owned")
			}
			if cli.holdsPark(token) || cli.isResuming(token) {
				t.Error("a queue that is never drained holds nothing and resumes nothing")
			}
			if len(rep.removed) != 0 || len(rep.retry) != 1 || rep.retry[0] != token {
				t.Errorf("its job fired and nobody will resume it: kept for retry, got %+v", rep)
			}
		})
	}
}

// A wake aimed, through the hub, at a park whose owner is already resuming
// it is dropped by that owner. The caller must hear that — not that the
// agent "resumes with outcome=cancelled".
func TestWakeOfAParkItsOwnerIsAlreadyResumingSaysSo(t *testing.T) {
	cli := resumeCLI(t, nil)
	var requested []string
	setRemoteParkProvider(&remoteParkProvider{
		owner:       func(string) (remoteParkOwner, bool) { return remoteParkOwner{Instance: "abcd", Origin: "repl"}, true },
		requestWake: func(token, _, _ string) bool { requested = append(requested, token); return true },
	})
	t.Cleanup(func() { setRemoteParkProvider(nil) })
	adapter := newLiveParkAdapter(cli)

	// Its job fired: the resume is queued in its owner.
	fired := saveParkAged(t, 0, enqueueParkJob(t, cli, true))
	// Its owner is running the resume right now.
	running := saveParkAged(t, 0, enqueueParkJob(t, cli, true))
	if err := park.Update(running, func(s *park.Snapshot) error { s.ResumeState = park.ResumeRunning; return nil }); err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"queued in its owner": fired, "running in its owner": running} {
		for verb, call := range map[string]func() (string, error){
			"resume": func() (string, error) { return adapter.ResumePark(token) },
			"cancel": func() (string, error) { return adapter.CancelPark(token, "x") },
		} {
			out, err := call()
			if err == nil || !strings.Contains(err.Error(), "already") || strings.Contains(out+err.Error(), "outcome=") {
				t.Errorf("%s of a park %s must say a resume is already under way: %q %v", verb, name, out, err)
			}
		}
	}
	if len(requested) != 0 {
		t.Fatalf("no wake request should have been left for parks already resuming: %v", requested)
	}
	// A park still waiting in its owner is woken there, as before.
	waiting := saveParkAged(t, 0, enqueueParkJob(t, cli, false))
	if out, err := adapter.CancelPark(waiting, "x"); err != nil || !strings.Contains(out, "outcome=cancelled") || len(requested) != 1 {
		t.Fatalf("a waiting park is woken through the hub: %q %v", out, err)
	}
}
