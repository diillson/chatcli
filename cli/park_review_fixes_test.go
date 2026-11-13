/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/scheduler"
	"go.uber.org/zap"
)

// resumeCLI is a ChatCLI with a scheduler whose resumed runs go through the
// real path — drain or inline wait, RunResumed, the snapshot bookkeeping —
// with only the LLM loop replaced by loop.
func resumeCLI(t *testing.T, loop func(ctx context.Context) error) *ChatCLI {
	t.Helper()
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := newCLIWithScheduler(t)
	cli.logger = zap.NewNop()
	cli.schedulerBridge = &schedulerBridge{cli: cli}
	cli.agentMode = &AgentMode{cli: cli, logger: zap.NewNop(), resumeLoop: loop}
	plugins.SetParkCanceller(newLiveParkAdapter(cli))
	t.Cleanup(func() { plugins.SetParkCanceller(nil) })
	return cli
}

// firedPark is an old REPL park whose job already fired: registered as
// active, its scheduler job terminal.
func firedPark(t *testing.T, cli *ChatCLI) string {
	t.Helper()
	token := saveParkAged(t, time.Hour, enqueueParkJob(t, cli, true))
	cli.registerActivePark(token, "10:00:00")
	return token
}

// Two parks fire while the terminal is busy; the drain takes both off the
// queue and resumes the first. A prune issued from inside that resumed run
// (the model's @park prune, or /parked prune typed mid-run) must leave
// both snapshots alone: the first is being resumed, the second is what the
// drain loads next. Both jobs are terminal by then — they fired.
func TestPruneDuringADrainKeepsTheParksBeingResumed(t *testing.T) {
	var cli *ChatCLI
	var first, second, pruneOut string
	runs := 0
	cli = resumeCLI(t, func(ctx context.Context) error {
		if runs++; runs > 1 {
			return nil
		}
		// Both ways a prune reaches the sweep mid-run.
		out, err := plugins.NewBuiltinParkPlugin().Execute(ctx, []string{"prune"})
		if err != nil {
			return err
		}
		pruneOut = out
		cli.parkPrune()
		if !parkOnDisk(first) || !parkOnDisk(second) {
			return errors.New("the prune deleted a park the drain still needs")
		}
		return nil
	})
	first, second = firedPark(t, cli), firedPark(t, cli)
	cli.pendingResumeQueue = []string{first, second}
	_ = captureStdout(t, func() { cli.drainPendingResumes(context.Background()) })

	if runs != 2 {
		t.Fatalf("both parks must resume; %d did", runs)
	}
	if !strings.HasPrefix(pruneOut, "pruned 0 ") || !strings.Contains(pruneOut, parkShort(first)) || !strings.Contains(pruneOut, parkShort(second)) {
		t.Fatalf("the prune must name both as kept: %q", pruneOut)
	}
	if parkOnDisk(first) || parkOnDisk(second) {
		t.Fatal("a resume that completed retires its snapshot")
	}
}

// F10: resume or cancel aimed at a token the drain already took must not
// queue it a second time.
func TestWakeOfAParkTheDrainTookDoesNotResumeItTwice(t *testing.T) {
	var cli *ChatCLI
	var a, b string
	var wakeErrs []error
	runs := map[string]int{}
	cli = resumeCLI(t, func(context.Context) error {
		if runs["total"]++; runs["total"] == 1 {
			adapter := newLiveParkAdapter(cli)
			_, rerr := adapter.ResumePark(b)
			_, cerr := adapter.CancelPark(b, "x")
			_, serr := adapter.ResumePark(a) // the one resuming right now
			wakeErrs = []error{rerr, cerr, serr}
		}
		return nil
	})
	a, b = firedPark(t, cli), firedPark(t, cli)
	cli.pendingResumeQueue = []string{a, b}
	_ = captureStdout(t, func() {
		for cli.drainPendingResumes(context.Background()) {
		}
	})
	for i, err := range wakeErrs {
		if err == nil || !strings.Contains(err.Error(), "already resuming") {
			t.Errorf("wake #%d of a park the drain holds must be refused: %v", i, err)
		}
	}
	if runs["total"] != 2 || len(cli.pendingResumeQueue) != 0 {
		t.Fatalf("each park resumes exactly once: %d runs, queue %v", runs["total"], cli.pendingResumeQueue)
	}
}

// F1: on an unattended surface a park waits in-turn. However that wait
// ends — resumed, failed, panicked, snapshot gone — this process stops
// holding the token; while the resumed cycle runs it still does.
func TestInlineParkIsReleasedOnEveryExit(t *testing.T) {
	for name, loop := range map[string]func(context.Context) error{
		"resumed":     func(context.Context) error { return nil },
		"failed":      func(context.Context) error { return errors.New("api down") },
		"panic":       func(context.Context) error { panic("boom") },
		"no snapshot": nil,
	} {
		t.Run(name, func(t *testing.T) {
			var cli *ChatCLI
			var token string
			heldMidRun := false
			cli = resumeCLI(t, func(ctx context.Context) error {
				heldMidRun = cli.holdsPark(token) && cli.parkStatusOf(mustLoadPark(t, token)).verdict == parkWaiting
				return loop(ctx)
			})
			cli.unattended = true
			a := cli.agentMode
			// What handleAgentPark leaves behind on an unattended surface.
			token = saveParkAged(t, time.Hour, enqueueParkJob(t, cli, true))
			ch := cli.registerParkWaiter(token)
			cli.registerActivePark(token, "10:00:00")
			a.inlineParkToken = token
			if loop == nil {
				_ = park.Delete(token)
			}
			ch <- parkOutcome{Outcome: "elapsed"}

			func() {
				defer func() { _ = recover() }()
				_ = a.runParkedInline(context.Background())
			}()
			if loop != nil && !heldMidRun {
				t.Error("while its resumed cycle runs the park must read as held")
			}
			if cli.ownsPark(token) || cli.holdsPark(token) {
				t.Fatal("the inline park is still held after its turn ended")
			}
		})
	}
}

func mustLoadPark(t *testing.T, token string) *park.Snapshot {
	t.Helper()
	snap, err := park.Load(token)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// F2: a resume that failed keeps its snapshot for /resume, and the CLI
// says so. That snapshot is not "finished": the sweep keeps it and names
// it with the way to retry. A resume in progress — typed by hand, so no
// drain marked it — is held; one that completed by re-parking is finished.
func TestSweepKeepsAParkWhoseResumeFailed(t *testing.T) {
	var cli *ChatCLI
	var token string
	var midRun parkSweepReport
	fail := errors.New("provider unavailable")
	cli = resumeCLI(t, func(context.Context) error {
		midRun = cli.sweepDeadParks(0)
		return fail
	})
	token = firedPark(t, cli)
	out := captureStdout(t, func() { cli.handleResumeCommand(context.Background(), "/resume "+token[:10]) })
	if len(midRun.removed) != 0 || len(midRun.live) != 1 {
		t.Fatalf("a park being resumed by hand must be held: %+v", midRun)
	}
	if !parkOnDisk(token) || !strings.Contains(out, parkShort(token)) {
		t.Fatalf("a failed resume keeps the snapshot and says how to retry: %q", out)
	}
	rep := cli.sweepDeadParks(0)
	if len(rep.removed) != 0 || !parkOnDisk(token) {
		t.Fatalf("the sweep deleted a snapshot the user was told to /resume: %+v", rep)
	}
	if len(rep.retry) != 1 || rep.retry[0] != token {
		t.Fatalf("it must be reported as kept for retry: %+v", rep)
	}
	res, err := plugins.NewBuiltinParkPlugin().Execute(context.Background(), []string{"prune"})
	if err != nil || !strings.Contains(res, "/resume "+parkShort(token)) || !parkOnDisk(token) {
		t.Fatalf("the tool must keep it and point at /resume: %q %v", res, err)
	}

	// The retry succeeds by parking again: the old snapshot is done.
	cli.agentMode.resumeLoop = func(context.Context) error { return errAgentParkedRequested }
	_ = captureStdout(t, func() { cli.handleResumeCommand(context.Background(), "/resume "+token) })
	if st := cli.parkStatusOf(mustLoadPark(t, token)); st.verdict != parkFinished {
		t.Fatalf("a resume that completed (it re-parked) leaves a finished snapshot, got %+v", st)
	}
	if rep = cli.sweepDeadParks(0); len(rep.removed) != 1 {
		t.Fatalf("a completed resume's snapshot is prunable: %+v", rep)
	}
}

// F2, across processes: a snapshot stamped as being resumed by a live
// process is held by it; once that process is gone the resume never
// completed, and the snapshot is kept for retry — not deleted.
func TestSweepReadsAnotherProcessesResumeInProgress(t *testing.T) {
	cli := resumeCLI(t, nil)
	token := saveParkAged(t, time.Hour, enqueueParkJob(t, cli, true))
	snap := mustLoadPark(t, token)
	snap.LastResumeAt = time.Now()
	snap.ResumeState = park.ResumeRunning
	snap.ResumerPID, snap.ResumerHost, snap.ResumerPIDNS = fleetLivePID, parkHostname(), parkPIDNamespace()
	if err := snap.Save(); err != nil {
		t.Fatal(err)
	}
	prev := parkOwnerAlive
	parkOwnerAlive = func(pid int) bool { return pid == fleetLivePID }
	t.Cleanup(func() { parkOwnerAlive = prev })
	if rep := cli.sweepDeadParks(0); len(rep.live) != 1 || len(rep.removed) != 0 {
		t.Fatalf("a park another live process is resuming must be held: %+v", rep)
	}
	parkOwnerAlive = func(int) bool { return false }
	if rep := cli.sweepDeadParks(0); len(rep.retry) != 1 || len(rep.removed) != 0 || !parkOnDisk(token) {
		t.Fatalf("a resume its process died in is kept for retry: %+v", rep)
	}
}

// sharedSchedulerCLI is a ChatCLI over a scheduler on dir — one of several
// processes sharing the scheduler's data directory.
func sharedSchedulerCLI(t *testing.T, dir string) *ChatCLI {
	t.Helper()
	cfg := scheduler.DefaultConfig()
	cfg.DataDir = dir
	cfg.AuditEnabled = false
	cfg.SnapshotInterval = 0
	cfg.WALGCInterval = 0
	cfg.DaemonAutoConnect = false
	s, err := scheduler.New(cfg, scheduler.NewNoopBridge(), scheduler.SchedulerDeps{}, nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("scheduler.Start: %v", err)
	}
	t.Cleanup(func() { s.DrainAndShutdown(2 * time.Second) })
	return &ChatCLI{scheduler: s, logger: zap.NewNop()}
}

// F3: terminal B is running; terminal A starts, parks for hours and
// closes. A's job is in the scheduler store both share, and fires when
// chatcli next replays it. B never loaded that job and A's process is
// gone — neither makes the park finished: the job is still pending.
func TestDeadOwnerWithAPendingStoredJobIsStillWaiting(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	dir := t.TempDir()
	b := sharedSchedulerCLI(t, dir) // started first: never loads A's job
	a := sharedSchedulerCLI(t, dir)
	prev := parkOwnerAlive
	parkOwnerAlive = func(int) bool { return false } // A has closed
	t.Cleanup(func() { parkOwnerAlive = prev })

	pending := saveParkOwnedBy(t, fleetDeadPID)
	setParkJob(t, pending, enqueueParkJob(t, a, false))
	fired := saveParkOwnedBy(t, fleetDeadPID)
	setParkJob(t, fired, enqueueParkJob(t, a, true))
	gone := saveParkOwnedBy(t, fleetDeadPID) // its job was never in this store
	a.scheduler.DrainAndShutdown(2 * time.Second)

	if _, err := b.scheduler.Query(scheduler.JobID(mustLoadPark(t, pending).SchedulerJobID)); err == nil {
		t.Fatal("the test needs B not to have loaded A's job")
	}
	rep := b.sweepDeadParks(0)
	if !parkOnDisk(pending) || sortedJoin(rep.live...) != pending {
		t.Fatalf("a dead owner's park with a pending stored job must be kept as waiting: %+v", rep)
	}
	if sortedJoin(rep.removed...) != sortedJoin(fired, gone) {
		t.Fatalf("with no pending job in the store a dead owner's park is finished: %+v", rep)
	}

	// A process that cannot read the store cannot tell: unknown, kept.
	blind := &ChatCLI{logger: zap.NewNop()}
	orphan := saveParkOwnedBy(t, fleetDeadPID)
	if rep := blind.sweepDeadParks(0); len(rep.removed) != 0 || !parkOnDisk(orphan) {
		t.Fatalf("without a scheduler store to ask, a dead owner proves nothing: %+v", rep)
	}
}

func setParkJob(t *testing.T, token, jobID string) {
	t.Helper()
	snap := mustLoadPark(t, token)
	snap.SchedulerJobID = jobID
	if err := snap.Save(); err != nil {
		t.Fatal(err)
	}
}
