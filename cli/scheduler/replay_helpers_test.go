package scheduler

import (
	"testing"
	"time"
)

func TestAdjustNextFireAt_DefaultForwardsToNow(t *testing.T) {
	s, _ := newAdmitScheduler(t)
	now := time.Now()

	// A relative-once job with a past NextFireAt and the default miss policy
	// should be re-armed to fire now.
	j := NewJob("past-job", Owner{Kind: OwnerUser, ID: "u"},
		Schedule{Kind: ScheduleRelative, Relative: time.Minute, MissPolicy: MissFireOnce},
		Action{Type: ActionNoop, Payload: map[string]any{}},
	)
	j.NextFireAt = now.Add(-time.Hour)

	if ok := s.adjustNextFireAt(j, now); !ok {
		t.Fatal("expected adjustNextFireAt to keep the job")
	}
	if j.NextFireAt.Before(now) {
		t.Errorf("NextFireAt=%v should be >= now", j.NextFireAt)
	}
}

func TestAdjustNextFireAt_SkipTerminalSchedule(t *testing.T) {
	s, _ := newAdmitScheduler(t)
	now := time.Now()

	// A past absolute (one-shot) schedule under MissSkip has no future
	// natural fire, so the job is dropped from the queue.
	j := NewJob("skip-job", Owner{Kind: OwnerUser, ID: "u"},
		Schedule{Kind: ScheduleAbsolute, ExactTime: now.Add(-time.Hour), MissPolicy: MissSkip},
		Action{Type: ActionNoop, Payload: map[string]any{}},
	)
	j.NextFireAt = now.Add(-time.Hour)

	if ok := s.adjustNextFireAt(j, now); ok {
		t.Error("expected MissSkip one-shot job to be dropped")
	}
}

func TestAdjustNextFireAt_FutureFireUnchanged(t *testing.T) {
	s, _ := newAdmitScheduler(t)
	now := time.Now()
	future := now.Add(time.Hour)

	j := NewJob("future-job", Owner{Kind: OwnerUser, ID: "u"},
		Schedule{Kind: ScheduleRelative, Relative: time.Minute},
		Action{Type: ActionNoop, Payload: map[string]any{}},
	)
	j.NextFireAt = future

	if ok := s.adjustNextFireAt(j, now); !ok {
		t.Fatal("future job should be kept")
	}
	if !j.NextFireAt.Equal(future) {
		t.Errorf("NextFireAt changed: %v want %v", j.NextFireAt, future)
	}
}

func TestGCOnce_ReapsExpiredTerminalJobs(t *testing.T) {
	s, _ := newAdmitScheduler(t)
	now := time.Now()

	// Expired terminal job.
	expired := NewJob("done", Owner{Kind: OwnerUser, ID: "u"},
		Schedule{Kind: ScheduleManual},
		Action{Type: ActionNoop, Payload: map[string]any{}},
	)
	expired.ID = JobID("expired-1")
	expired.Status = StatusCompleted
	expired.FinishedAt = now.Add(-2 * time.Hour)
	expired.TTL = time.Hour

	// Fresh terminal job (not yet expired).
	fresh := NewJob("recent", Owner{Kind: OwnerUser, ID: "u"},
		Schedule{Kind: ScheduleManual},
		Action{Type: ActionNoop, Payload: map[string]any{}},
	)
	fresh.ID = JobID("fresh-1")
	fresh.Status = StatusCompleted
	fresh.FinishedAt = now
	fresh.TTL = time.Hour

	s.mu.Lock()
	s.jobs[expired.ID] = expired
	s.jobs[fresh.ID] = fresh
	s.mu.Unlock()

	s.gcOnce()

	s.mu.RLock()
	_, hasExpired := s.jobs[expired.ID]
	_, hasFresh := s.jobs[fresh.ID]
	s.mu.RUnlock()

	if hasExpired {
		t.Error("expired job should have been reaped")
	}
	if !hasFresh {
		t.Error("fresh job should survive gc")
	}
}

func TestWriteSnapshotNow(t *testing.T) {
	s, _ := newAdmitScheduler(t)

	j := NewJob("snap", Owner{Kind: OwnerUser, ID: "u"},
		Schedule{Kind: ScheduleManual},
		Action{Type: ActionNoop, Payload: map[string]any{}},
	)
	j.ID = JobID("snap-1")
	s.mu.Lock()
	s.jobs[j.ID] = j
	s.mu.Unlock()

	if err := s.writeSnapshotNow(); err != nil {
		t.Fatalf("writeSnapshotNow: %v", err)
	}
}
