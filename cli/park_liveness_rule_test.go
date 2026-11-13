//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/i18n"
)

// A park another live process owns, with its job still pending in the
// shared store: the owner comes first. The sweep keeps it, and a note for
// it — by the command or the tool — is refused, because its owner rewrites
// the snapshot. Once the owner is gone the pending job still makes it a
// waiting park (a scheduler that replays the store will fire it), and a
// note can then be recorded here.
func TestLiveOwnerComesBeforeItsPendingJob(t *testing.T) {
	b, a, dir := twoTerminals(t, "long", nil)
	token := waitForFile(t, dir, "token", 60*time.Second)
	if job, err := b.scheduler.StoredJob(scheduler.JobID(mustLoadPark(t, token).SchedulerJobID)); err != nil || job.Status.IsTerminal() {
		t.Fatalf("the test needs the owner's job pending in the shared store: %v", err)
	}

	sweepBothWays(t, b, token, "with a live owner and a pending job")
	out := captureStdout(t, func() { b.handleParkNoteCommand("--token " + token + " from the other terminal") })
	if strings.Contains(out, i18n.T("leftovers.park_note.queued", parkShort(token), parkShort(token))) || parkDirectives(t, token) != "" {
		t.Fatalf("/park-note for a park another live process owns was accepted: %q", out)
	}
	if _, err := plugins.NewBuiltinParkPlugin().Execute(context.Background(), []string{"note", "--text", "from the model", "--token", token[:12]}); err == nil || !strings.Contains(err.Error(), "another live process") || parkDirectives(t, token) != "" {
		t.Fatalf("@park note for a park another live process owns must be refused: %v", err)
	}

	if err := a.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = a.Process.Wait()
	rep := b.sweepDeadParks(0)
	if len(rep.removed) != 0 || len(rep.live) != 1 || !parkOnDisk(token) {
		t.Fatalf("owner gone, job still pending in the store: the park still waits: %+v", rep)
	}
	if out = captureStdout(t, func() { b.handleParkNoteCommand("--token " + token + " now it is nobody's") }); parkDirectives(t, token) != "now it is nobody's" {
		t.Fatalf("with no live owner the note is recorded: %q", out)
	}
}

// legacyParkSnapshot writes a snapshot as released versions did: no owner,
// no resume state — only that a resume started.
func legacyParkSnapshot(t *testing.T, jobID string, resumed bool) string {
	t.Helper()
	snap := &park.Snapshot{Token: park.NewToken(), CreatedAt: time.Now().Add(-48 * time.Hour), SchedulerJobID: jobID,
		Park: park.Request{Mode: park.ModeDelay, Delay: time.Hour}}
	if resumed {
		snap.LastResumeAt = time.Now().Add(-47 * time.Hour)
	}
	if err := snap.Save(); err != nil {
		t.Fatal(err)
	}
	return snap.Token
}

// Snapshots written before the resume outcome was recorded. Released
// versions kept one per monitoring cycle, with LastResumeAt set and the
// job finished, and /parked prune removed exactly those. It still does:
// for a snapshot with none of the new fields, a resume that started, a
// finished job and no live owner mean it completed under the old code.
func TestLegacyResumedSnapshotsAreStillPruned(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	dir := t.TempDir()
	b := sharedSchedulerCLI(t, dir)
	other := sharedSchedulerCLI(t, dir) // another terminal's scheduler, same store

	done := legacyParkSnapshot(t, enqueueParkJob(t, b, true), true)
	doneElsewhere := legacyParkSnapshot(t, enqueueParkJob(t, other, true), true)
	stillPending := legacyParkSnapshot(t, enqueueParkJob(t, other, false), true)
	jobUnknown := legacyParkSnapshot(t, "job-purged-long-ago", true)
	neverResumed := legacyParkSnapshot(t, enqueueParkJob(t, b, true), false)
	owned := legacyParkSnapshot(t, enqueueParkJob(t, other, true), true)
	setRemoteParkProvider(&remoteParkProvider{owner: func(token string) (remoteParkOwner, bool) {
		return remoteParkOwner{Instance: "other"}, token == owned
	}})
	t.Cleanup(func() { setRemoteParkProvider(nil) })
	other.scheduler.DrainAndShutdown(2 * time.Second)

	rep := b.sweepDeadParks(0)
	if sortedJoin(rep.removed...) != sortedJoin(done, doneElsewhere) {
		t.Fatalf("legacy snapshots of completed cycles must be pruned as before: removed %v", parkShorts(rep.removed))
	}
	for name, token := range map[string]string{"job still pending": stillPending, "job unknown": jobUnknown, "never resumed": neverResumed, "live owner": owned} {
		if !parkOnDisk(token) {
			t.Errorf("legacy snapshot (%s) must be kept", name)
		}
	}
	// A legacy snapshot records no owner: with its job fired and no
	// resume, the owner may be alive with the resume queued. Unknown.
	if sortedJoin(rep.unknown...) != sortedJoin(jobUnknown, neverResumed) || len(rep.retry) != 0 {
		t.Errorf("unknown: %v, retry: %v", parkShorts(rep.unknown), parkShorts(rep.retry))
	}
}

// A resume that is refused before it starts — another run is in flight —
// tells the user to retry with /resume. The snapshot says the same.
func TestRefusedResumeIsStampedForRetry(t *testing.T) {
	cli := resumeCLI(t, func(context.Context) error {
		t.Fatal("the resume must have been refused before the loop")
		return nil
	})
	token := firedPark(t, cli)
	cli.agentMode.runInflight.Store(true) // another run holds this agent
	out := captureStdout(t, func() { cli.handleResumeCommand(context.Background(), "/resume "+token) })
	cli.agentMode.runInflight.Store(false)
	if !strings.Contains(out, i18n.T("park.resume.retry_hint", parkShort(token))) {
		t.Fatalf("the user must be told to retry: %q", out)
	}
	if state := mustLoadPark(t, token).ResumeState; state != park.ResumeRetry {
		t.Fatalf("the snapshot must record what the message says, resume_state is %q", state)
	}
	if rep := cli.sweepDeadParks(0); len(rep.retry) != 1 || len(rep.removed) != 0 {
		t.Fatalf("kept for retry: %+v", rep)
	}
}

// A pid is reused. The snapshot records when its owner started: a live
// process under the same pid with another start time is not the owner.
// The process here is real and its start time is read by the real probe;
// only the snapshot's stale start time is written by hand — a pid cannot
// be made to wrap on request.
func TestReusedPidIsNotTheOwner(t *testing.T) {
	start, ok := parkProcessStart(1)
	if !ok {
		t.Skip("this platform does not give a process start time cheaply")
	}
	if again, _ := parkProcessStart(1); again != start || start == "" {
		t.Fatalf("a process start time must be stable: %q then %q", start, again)
	}
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := sharedSchedulerCLI(t, t.TempDir())
	sleeper := exec.Command("sleep", "120")
	if err := sleeper.Start(); err != nil {
		t.Skipf("no process to stand in for the owner: %v", err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _, _ = sleeper.Process.Wait() })
	pid := sleeper.Process.Pid
	realStart, ok := parkProcessStart(pid)
	if !ok {
		t.Fatal("no start time for a process this test just started")
	}
	save := func(start string) string {
		snap := &park.Snapshot{Token: park.NewToken(), CreatedAt: time.Now().Add(-48 * time.Hour),
			SchedulerJobID: enqueueParkJob(t, cli, true),
			OwnerPID:       pid, OwnerHost: parkHostname(), OwnerPIDNS: parkPIDNamespace(), OwnerStart: start,
			Park: park.Request{Mode: park.ModeDelay, Delay: time.Hour}}
		if err := snap.Save(); err != nil {
			t.Fatal(err)
		}
		return snap.Token
	}
	sameProcess, reusedPid, noStartRecorded := save(realStart), save("started-some-other-time"), save("")
	rep := cli.sweepDeadParks(0)
	if sortedJoin(rep.live...) != sortedJoin(sameProcess, noStartRecorded) {
		t.Fatalf("the owner's own process, and one whose start was never recorded, are alive: %v", parkShorts(rep.live))
	}
	if sortedJoin(rep.retry...) != reusedPid {
		t.Fatalf("a pid now held by another process is a dead owner (job fired, never resumed: kept for retry): %+v", rep)
	}
}
