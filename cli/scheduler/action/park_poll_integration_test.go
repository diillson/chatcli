/*
 * Integration tests for the @park polling loop against a REAL
 * scheduler. The unit tests in park_poll_test.go stub env.Enqueue, so
 * they never exercise Scheduler.Enqueue's duplicate-name admission —
 * which is exactly where the poll chain used to die: the re-scheduled
 * iteration reused the live job's name and was rejected with
 * ErrDuplicateName on the very first non-matching probe.
 */
package action

import (
	"context"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
)

// newParkScheduler boots a real scheduler wired to the fake bridge with
// only the park executors registered.
func newParkScheduler(t *testing.T, b *fakeBridge) *scheduler.Scheduler {
	t.Helper()
	cfg := scheduler.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.AuditEnabled = false
	cfg.SnapshotInterval = 0
	cfg.WALGCInterval = 0
	cfg.DaemonAutoConnect = false

	s, err := scheduler.New(cfg, b, scheduler.SchedulerDeps{}, nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	if err := s.Actions().Register(NewParkPoll()); err != nil {
		t.Fatalf("register park_poll: %v", err)
	}
	if err := s.Actions().Register(NewAgentResume()); err != nil {
		t.Fatalf("register agent_resume: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := s.Start(ctx); err != nil {
		t.Fatalf("scheduler start: %v", err)
	}
	t.Cleanup(func() { s.DrainAndShutdown(2 * time.Second) })
	return s
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

func parkPollAction(token, interval string, deadline time.Time) scheduler.Action {
	return scheduler.Action{
		Type: scheduler.ActionParkPoll,
		Payload: map[string]any{
			"resume_token":  token,
			"mode":          "for_cmd",
			"command":       "check-status",
			"interval":      interval,
			"deadline_unix": deadline.Unix(),
			"success_when":  "exit=0",
		},
	}
}

// TestParkPoll_RealScheduler_RearmsAcrossCyclesAndStopsOnMatch is the
// user-reported scenario: a for_cmd park whose probe does NOT match on
// the first firing must keep polling (it used to fail with "scheduler:
// duplicate name" on the first re-schedule), then fire AgentResume once
// the probe matches — and the recurring job must finalize instead of
// polling forever.
func TestParkPoll_RealScheduler_RearmsAcrossCyclesAndStopsOnMatch(t *testing.T) {
	b := &fakeBridge{cmdExit: 1}
	b.cmdMatchAfter.Store(3) // probes 1-2 miss, probe 3 matches
	s := newParkScheduler(t, b)

	const token = "tok-int-rearm"
	job := scheduler.NewJob(
		"park-poll:"+token,
		scheduler.Owner{Kind: "park", ID: "agent", Tag: token},
		scheduler.Schedule{Kind: scheduler.ScheduleInterval, Interval: 80 * time.Millisecond},
		parkPollAction(token, "80ms", time.Now().Add(time.Minute)),
	)
	job.DangerousConfirmed = true

	created, err := s.Enqueue(context.Background(), job)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	waitFor(t, 10*time.Second, "AgentResume to notify the bridge", func() bool {
		return b.notifyCalls.Load() >= 1
	})
	if b.notifyOut != "matched" {
		t.Fatalf("resume outcome: want matched, got %q", b.notifyOut)
	}
	if b.notifyTok != token {
		t.Fatalf("resume token: want %q, got %q", token, b.notifyTok)
	}
	if got := b.cmdCalls.Load(); got < 3 {
		t.Fatalf("poll must survive re-scheduling across cycles: want >=3 probes, got %d", got)
	}

	// The whole poll must have run on the SAME job record (in-place
	// re-arm), and the match must stop the recurrence.
	waitFor(t, 5*time.Second, "poll job to reach a terminal status", func() bool {
		j, qerr := s.Query(created.ID)
		return qerr == nil && j.Status.IsTerminal()
	})
	j, err := s.Query(created.ID)
	if err != nil {
		t.Fatalf("query poll job: %v", err)
	}
	if j.Status != scheduler.StatusCompleted {
		t.Fatalf("poll job status: want %s, got %s (last error: %+v)", scheduler.StatusCompleted, j.Status, j.LastResult)
	}
}

// TestParkPoll_RealScheduler_LegacyRelativeJob_KeepsPolling covers
// pre-upgrade jobs replayed from the WAL: they still carry the old
// one-shot relative schedule, so the executor must fall back to sibling
// re-scheduling — with a name the admission check accepts.
func TestParkPoll_RealScheduler_LegacyRelativeJob_KeepsPolling(t *testing.T) {
	b := &fakeBridge{cmdExit: 1}
	b.cmdMatchAfter.Store(2) // probe 1 misses, probe 2 matches
	s := newParkScheduler(t, b)

	const token = "tok-legacy-poll"
	job := scheduler.NewJob(
		"park-poll:"+token,
		scheduler.Owner{Kind: "park", ID: "agent", Tag: token},
		scheduler.Schedule{Kind: scheduler.ScheduleRelative, Relative: 60 * time.Millisecond},
		parkPollAction(token, "60ms", time.Now().Add(time.Minute)),
	)
	job.DangerousConfirmed = true

	if _, err := s.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	waitFor(t, 10*time.Second, "AgentResume to notify the bridge", func() bool {
		return b.notifyCalls.Load() >= 1
	})
	if b.notifyOut != "matched" {
		t.Fatalf("resume outcome: want matched, got %q", b.notifyOut)
	}
	if got := b.cmdCalls.Load(); got < 2 {
		t.Fatalf("legacy poll must keep polling: want >=2 probes, got %d", got)
	}
}
