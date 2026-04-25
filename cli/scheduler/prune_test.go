/*
 * Tests for Scheduler.Prune (manual cleanup of terminal-state jobs)
 * and for the List filter regression where --status <terminal> always
 * returned an empty table because the IncludeTerminal default gated
 * out terminal jobs before the Statuses filter could match.
 */
package scheduler

import (
	"context"
	"testing"
	"time"
)

func TestPrune_RemovesTerminalJobs_KeepsActive(t *testing.T) {
	s := newTestScheduler(t, &policyStubBridge{})
	defer s.DrainAndShutdown(2 * time.Second)

	// Build three jobs and force statuses by direct mutation; using
	// Enqueue + waiting for natural firings would make this test slow
	// and racy. Cancel sets terminal status legitimately, so we use it
	// for the two we want pruned.
	pendingJob := testShellJob("keep-pending", "echo keep", true)
	pendingJob.Schedule.Relative = 10 * time.Hour // never fires during test
	if _, err := s.Enqueue(context.Background(), pendingJob); err != nil {
		t.Fatalf("enqueue pending: %v", err)
	}

	failedJob := testShellJob("drop-failed", "echo drop1", true)
	failedJob.Schedule.Relative = 10 * time.Hour
	if _, err := s.Enqueue(context.Background(), failedJob); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	if err := s.Cancel(failedJob.ID, "test", failedJob.Owner); err != nil {
		t.Fatalf("cancel failed-job: %v", err)
	}

	completedJob := testShellJob("drop-cancelled", "echo drop2", true)
	completedJob.Schedule.Relative = 10 * time.Hour
	if _, err := s.Enqueue(context.Background(), completedJob); err != nil {
		t.Fatalf("enqueue completed: %v", err)
	}
	if err := s.Cancel(completedJob.ID, "test", completedJob.Owner); err != nil {
		t.Fatalf("cancel completed-job: %v", err)
	}

	removed, err := s.Prune(PruneFilter{})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed %d, want 2 (the two cancelled jobs)", len(removed))
	}

	// Pending job must still be present.
	if _, err := s.Query(pendingJob.ID); err != nil {
		t.Fatalf("pending job vanished after prune: %v", err)
	}
	// Pruned jobs must be gone.
	if _, err := s.Query(failedJob.ID); err != ErrJobNotFound {
		t.Fatalf("failed job still queryable after prune: %v", err)
	}
}

func TestPrune_StatusFilter_NarrowsScope(t *testing.T) {
	s := newTestScheduler(t, &policyStubBridge{})
	defer s.DrainAndShutdown(2 * time.Second)

	// Enqueue + cancel three jobs, all end up status=cancelled. We
	// then ask Prune to remove only status=failed — should remove
	// nothing because no job has that status.
	for _, name := range []string{"a", "b", "c"} {
		j := testShellJob(name, "echo "+name, true)
		j.Schedule.Relative = 10 * time.Hour
		if _, err := s.Enqueue(context.Background(), j); err != nil {
			t.Fatalf("enqueue %s: %v", name, err)
		}
		if err := s.Cancel(j.ID, "test", j.Owner); err != nil {
			t.Fatalf("cancel %s: %v", name, err)
		}
	}

	removed, err := s.Prune(PruneFilter{Statuses: []JobStatus{StatusFailed}})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("status=failed should match nothing, got %d", len(removed))
	}

	// Now prune cancelled — should match all three.
	removed, err = s.Prune(PruneFilter{Statuses: []JobStatus{StatusCancelled}})
	if err != nil {
		t.Fatalf("prune cancelled: %v", err)
	}
	if len(removed) != 3 {
		t.Fatalf("status=cancelled removed %d, want 3", len(removed))
	}
}

// (CLI-level two-step UX is covered in cli package tests; this file
// only exercises the lib-level Prune/List behavior.)

func TestPrune_LeavesActiveAlone(t *testing.T) {
	s := newTestScheduler(t, &policyStubBridge{})
	defer s.DrainAndShutdown(2 * time.Second)

	// Even with a wide-open filter, pending/running/waiting/paused
	// jobs are NOT pruned — they are not terminal.
	for _, name := range []string{"p1", "p2"} {
		j := testShellJob(name, "echo "+name, true)
		j.Schedule.Relative = 10 * time.Hour
		if _, err := s.Enqueue(context.Background(), j); err != nil {
			t.Fatalf("enqueue %s: %v", name, err)
		}
	}

	removed, err := s.Prune(PruneFilter{})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("pending jobs were pruned (%d) — must never happen", len(removed))
	}
}

// TestList_StatusFilterOverridesIncludeTerminalDefault is the regression
// for the user report "--status filter sempre retorna tudo". Before the
// fix, `/jobs list --status failed` returned 0 rows because the
// IncludeTerminal=false default skipped every terminal entry BEFORE the
// Statuses filter could match. Now an explicit Statuses list overrides
// the default, so the user gets exactly what they asked for.
func TestList_StatusFilterOverridesIncludeTerminalDefault(t *testing.T) {
	s := newTestScheduler(t, &policyStubBridge{})
	defer s.DrainAndShutdown(2 * time.Second)

	// Create 1 active + 2 cancelled jobs.
	active := testShellJob("active", "echo a", true)
	active.Schedule.Relative = 10 * time.Hour
	if _, err := s.Enqueue(context.Background(), active); err != nil {
		t.Fatalf("enqueue active: %v", err)
	}
	for _, n := range []string{"x", "y"} {
		j := testShellJob(n, "echo "+n, true)
		j.Schedule.Relative = 10 * time.Hour
		if _, err := s.Enqueue(context.Background(), j); err != nil {
			t.Fatalf("enqueue %s: %v", n, err)
		}
		if err := s.Cancel(j.ID, "test", j.Owner); err != nil {
			t.Fatalf("cancel %s: %v", n, err)
		}
	}

	// Default list (no IncludeTerminal): only the active one.
	defaultList := s.List(ListFilter{})
	if len(defaultList) != 1 || defaultList[0].Name != "active" {
		t.Fatalf("default list = %d entries, want 1 (active); got: %+v", len(defaultList), defaultList)
	}

	// Explicit --status cancelled: must surface the two cancelled jobs
	// even though IncludeTerminal is false.
	cancelledList := s.List(ListFilter{Statuses: []JobStatus{StatusCancelled}})
	if len(cancelledList) != 2 {
		t.Fatalf("status=cancelled list = %d entries, want 2; got: %+v", len(cancelledList), cancelledList)
	}
	for _, sum := range cancelledList {
		if sum.Status != StatusCancelled {
			t.Errorf("entry %q has wrong status %q", sum.Name, sum.Status)
		}
	}

	// --status failed (no jobs in that state): empty.
	failedList := s.List(ListFilter{Statuses: []JobStatus{StatusFailed}})
	if len(failedList) != 0 {
		t.Fatalf("status=failed list = %d entries, want 0", len(failedList))
	}
}
