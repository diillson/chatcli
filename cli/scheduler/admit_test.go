package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

// newAdmitScheduler builds a non-started scheduler suitable for exercising
// Enqueue's admission + default-fill logic.
func newAdmitScheduler(t *testing.T) (*Scheduler, Config) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.AuditEnabled = false
	cfg.SnapshotInterval = 0
	cfg.WALGCInterval = 0
	cfg.DaemonAutoConnect = false

	s, err := New(cfg, NewNoopBridge(), SchedulerDeps{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DrainAndShutdown(time.Second) })
	return s, cfg
}

func noopJob(owner Owner) *Job {
	return NewJob("test-job", owner,
		Schedule{Kind: ScheduleRelative, Relative: time.Hour},
		Action{Type: ActionNoop, Payload: map[string]any{}},
	)
}

func TestAdmitJob_AgentsDisabled(t *testing.T) {
	s, cfg := newAdmitScheduler(t)
	cfg.AllowAgents = false
	s.cfg = cfg

	_, err := s.Enqueue(context.Background(), noopJob(Owner{Kind: OwnerAgent, ID: "a1"}))
	if !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("expected ErrNotAuthorized, got %v", err)
	}

	// Worker owner is also blocked.
	_, err = s.Enqueue(context.Background(), noopJob(Owner{Kind: OwnerWorker, ID: "w1"}))
	if !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("expected ErrNotAuthorized for worker, got %v", err)
	}
}

func TestAdmitJob_ActionDisallowed(t *testing.T) {
	s, cfg := newAdmitScheduler(t)
	// Allowlist that excludes noop.
	cfg.ActionAllowlist = map[ActionType]bool{ActionShell: true}
	s.cfg = cfg

	_, err := s.Enqueue(context.Background(), noopJob(Owner{Kind: OwnerUser, ID: "u1"}))
	if !errors.Is(err, ErrActionDisallowed) {
		t.Fatalf("expected ErrActionDisallowed, got %v", err)
	}
}

func TestEnqueue_FillsDefaults(t *testing.T) {
	s, _ := newAdmitScheduler(t)

	job := noopJob(Owner{Kind: OwnerUser, ID: "u1"})
	// Leave ID, timestamps, version, budget, TTL, history zero.
	created, err := s.Enqueue(context.Background(), job)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if created.ID.IsZero() {
		t.Error("ID should be derived")
	}
	if created.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
	if created.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set")
	}
	if created.Version != SchemaVersion {
		t.Errorf("Version=%d want %d", created.Version, SchemaVersion)
	}
	if created.TTL != s.cfg.DefaultTTL {
		t.Errorf("TTL=%v want %v", created.TTL, s.cfg.DefaultTTL)
	}
	if created.HistoryLimit != s.cfg.HistoryLimit {
		t.Errorf("HistoryLimit=%d want %d", created.HistoryLimit, s.cfg.HistoryLimit)
	}
	// Budget defaults merged in.
	if created.Budget.ActionTimeout != s.cfg.DefaultActionTimeout {
		t.Errorf("Budget.ActionTimeout=%v want %v", created.Budget.ActionTimeout, s.cfg.DefaultActionTimeout)
	}
}

func TestEnqueue_NilJob(t *testing.T) {
	s, _ := newAdmitScheduler(t)
	if _, err := s.Enqueue(context.Background(), nil); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("expected ErrInvalidJob, got %v", err)
	}
}
