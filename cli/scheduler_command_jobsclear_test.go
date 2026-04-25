/*
 * Tests the /jobs clear two-step destructive UX (preview without
 * --yes, delete with --yes). Driven by the user report on
 * 2026-04-24 where the original interactive [y/N] prompt deadlocked
 * the terminal because go-prompt holds stdin in raw mode during
 * executor execution.
 */
package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
)

// newCLIWithScheduler builds a minimal ChatCLI with a real Scheduler
// for jobsClear testing. We don't bring up the LLM client, agent
// mode, or hooks — jobsClear only touches cli.scheduler and the
// stdout printer. Cleanup runs scheduler shutdown.
func newCLIWithScheduler(t *testing.T) *ChatCLI {
	t.Helper()
	cfg := scheduler.DefaultConfig()
	cfg.DataDir = t.TempDir()
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
	return &ChatCLI{scheduler: s}
}

// seedTerminalJobs creates n jobs in the cancelled (terminal) state
// so prune has something to chew on. Schedule.Relative is set far
// in the future to avoid races with the dispatcher.
func seedTerminalJobs(t *testing.T, cli *ChatCLI, n int) []scheduler.JobID {
	t.Helper()
	ids := make([]scheduler.JobID, 0, n)
	for i := 0; i < n; i++ {
		j := scheduler.NewJob(
			"job-"+string(rune('a'+i)),
			scheduler.Owner{Kind: scheduler.OwnerUser, ID: "test"},
			scheduler.Schedule{Kind: scheduler.ScheduleRelative, Relative: 10 * time.Hour},
			scheduler.Action{
				Type:    scheduler.ActionShell,
				Payload: map[string]any{"command": "echo " + string(rune('a'+i))},
			},
		)
		j.DangerousConfirmed = true
		created, err := cli.scheduler.Enqueue(context.Background(), j)
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
		if err := cli.scheduler.Cancel(created.ID, "test seed", j.Owner); err != nil {
			t.Fatalf("cancel %d: %v", i, err)
		}
		ids = append(ids, created.ID)
	}
	return ids
}

func countTerminalJobs(cli *ChatCLI) int {
	list := cli.scheduler.List(scheduler.ListFilter{IncludeTerminal: true})
	n := 0
	for _, s := range list {
		if s.Status.IsTerminal() {
			n++
		}
	}
	return n
}

// TestJobsClear_PreviewWithoutYes is the regression for the user
// report ("/jobs clear ... fica preso o terminal sem conseguir
// digitar"). Without --yes the command must NOT block on stdin and
// must NOT delete anything — only show a preview and instruct the
// user to re-run with --yes.
func TestJobsClear_PreviewWithoutYes(t *testing.T) {
	cli := newCLIWithScheduler(t)
	seedTerminalJobs(t, cli, 3)

	if countTerminalJobs(cli) != 3 {
		t.Fatalf("seed failed: want 3 terminal jobs, got %d", countTerminalJobs(cli))
	}

	out := captureStdout(t, func() {
		// Bound the call so a regression that re-introduces stdin
		// prompting fails the test instead of hanging the runner.
		done := make(chan struct{})
		go func() {
			cli.jobsClear([]string{})
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("jobsClear blocked — must return synchronously without prompting for stdin")
		}
	})

	// Nothing deleted.
	if got := countTerminalJobs(cli); got != 3 {
		t.Errorf("preview deleted jobs: want 3 still present, got %d", got)
	}
	// Output advertises the two-step pattern.
	if !strings.Contains(out, "Would delete") || !strings.Contains(out, "--yes") {
		t.Errorf("preview output missing two-step instructions: %q", out)
	}
	// Output includes job count.
	if !strings.Contains(out, "3 terminal job") {
		t.Errorf("preview output missing job count: %q", out)
	}
}

// TestJobsClear_DeletesWithYes confirms the second leg of the
// two-step UX: --yes actually prunes.
func TestJobsClear_DeletesWithYes(t *testing.T) {
	cli := newCLIWithScheduler(t)
	seedTerminalJobs(t, cli, 4)

	out := captureStdout(t, func() {
		cli.jobsClear([]string{"--yes"})
	})

	if got := countTerminalJobs(cli); got != 0 {
		t.Errorf("--yes did not delete: want 0 terminal jobs, got %d", got)
	}
	if !strings.Contains(out, "removed 4 terminal job") {
		t.Errorf("output should confirm deletion count: %q", out)
	}
}

// TestJobsClear_StatusFilterScopes confirms that --failed only
// removes failed jobs (we seed cancelled, so nothing matches).
func TestJobsClear_StatusFilterScopes(t *testing.T) {
	cli := newCLIWithScheduler(t)
	seedTerminalJobs(t, cli, 2) // all cancelled

	captureStdout(t, func() {
		cli.jobsClear([]string{"--failed", "--yes"})
	})

	// Cancelled jobs should still be there; --failed didn't match.
	if got := countTerminalJobs(cli); got != 2 {
		t.Errorf("--failed deleted cancelled jobs: want 2 still present, got %d", got)
	}

	// Now --cancelled --yes should clear them.
	captureStdout(t, func() {
		cli.jobsClear([]string{"--cancelled", "--yes"})
	})
	if got := countTerminalJobs(cli); got != 0 {
		t.Errorf("--cancelled --yes failed to clean: %d still present", got)
	}
}

// TestJobsClear_EmptyFilterShowsHelpfulMessage: when no terminal jobs
// match, output should say so explicitly instead of pretending success.
func TestJobsClear_EmptyFilterShowsHelpfulMessage(t *testing.T) {
	cli := newCLIWithScheduler(t)
	// No jobs seeded.

	out := captureStdout(t, func() {
		cli.jobsClear([]string{"--yes"})
	})

	if !strings.Contains(out, "nothing to clear") {
		t.Errorf("expected 'nothing to clear' message; got: %q", out)
	}
}
