package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// fakeEvalAction is a ConditionEvaluator + ActionExecutor used to test
// the scheduler end-to-end without pulling in the real evaluators.
type fakeEval struct {
	gate atomic.Int32 // 0 = not satisfied, 1 = satisfied
	hits atomic.Int32
}

func (f *fakeEval) Type() string                        { return "fake" }
func (f *fakeEval) ValidateSpec(_ map[string]any) error { return nil }
func (f *fakeEval) Evaluate(_ context.Context, _ Condition, _ *EvalEnv) EvalOutcome {
	f.hits.Add(1)
	return EvalOutcome{Satisfied: f.gate.Load() == 1, Details: "fake"}
}

type fakeAct struct {
	calls atomic.Int32
}

func (f *fakeAct) Type() ActionType                    { return ActionType("fake_act") }
func (f *fakeAct) ValidateSpec(_ map[string]any) error { return nil }
func (f *fakeAct) Execute(_ context.Context, _ Action, _ *ExecEnv) ActionResult {
	f.calls.Add(1)
	return ActionResult{Output: "ran"}
}

func TestScheduler_EndToEnd_WaitThenAction(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.DataDir = dir
	cfg.DefaultPollInterval = 50 * time.Millisecond
	cfg.DefaultWaitTimeout = 5 * time.Second
	cfg.AuditEnabled = false
	cfg.SnapshotInterval = 0
	cfg.WALGCInterval = 0
	cfg.DaemonAutoConnect = false

	s, err := New(cfg, NewNoopBridge(), SchedulerDeps{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fe := &fakeEval{}
	fa := &fakeAct{}
	_ = s.Conditions().Register(fe)
	_ = s.Actions().Register(fa)
	cfg.ActionAllowlist[ActionType("fake_act")] = true
	s.cfg = cfg

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.DrainAndShutdown(2 * time.Second)

	job := NewJob("wait-and-go",
		Owner{Kind: OwnerUser, ID: "tester"},
		Schedule{Kind: ScheduleRelative, Relative: 50 * time.Millisecond},
		Action{Type: ActionType("fake_act"), Payload: map[string]any{}},
	)
	job.Wait = &WaitSpec{Condition: Condition{Type: "fake", Spec: map[string]any{}}}
	created, err := s.Enqueue(ctx, job)
	if err != nil {
		t.Fatal(err)
	}

	// Flip the condition after ~200ms.
	go func() {
		time.Sleep(200 * time.Millisecond)
		fe.gate.Store(1)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		q, err := s.Query(created.ID)
		if err == nil && q.Status == StatusCompleted {
			if fa.calls.Load() != 1 {
				t.Errorf("expected action called once, got %d", fa.calls.Load())
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("job never completed; last hits=%d calls=%d", fe.hits.Load(), fa.calls.Load())
}

func TestScheduler_Cancel(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.DataDir = dir
	cfg.AuditEnabled = false
	cfg.SnapshotInterval = 0
	cfg.WALGCInterval = 0
	cfg.DaemonAutoConnect = false

	s, err := New(cfg, NewNoopBridge(), SchedulerDeps{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Actions().Register(&fakeAct{})
	cfg.ActionAllowlist[ActionType("fake_act")] = true
	s.cfg = cfg

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.DrainAndShutdown(2 * time.Second)

	j := NewJob("cancel-me", Owner{Kind: OwnerUser, ID: "u"},
		Schedule{Kind: ScheduleRelative, Relative: 10 * time.Second},
		Action{Type: ActionType("fake_act")})
	c, err := s.Enqueue(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(c.ID, "test", Owner{Kind: OwnerUser, ID: "u"}); err != nil {
		t.Fatal(err)
	}
	q, _ := s.Query(c.ID)
	if q.Status != StatusCancelled {
		t.Errorf("want cancelled, got %s", q.Status)
	}
}

func TestScheduler_DAG_DependsOn(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.DataDir = dir
	cfg.AuditEnabled = false
	cfg.SnapshotInterval = 0
	cfg.WALGCInterval = 0
	cfg.DaemonAutoConnect = false

	s, err := New(cfg, NewNoopBridge(), SchedulerDeps{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fa := &fakeAct{}
	_ = s.Actions().Register(fa)
	cfg.ActionAllowlist[ActionType("fake_act")] = true
	s.cfg = cfg

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = s.Start(ctx)
	defer s.DrainAndShutdown(2 * time.Second)

	parent := NewJob("p", Owner{Kind: OwnerUser, ID: "u"},
		Schedule{Kind: ScheduleRelative, Relative: 50 * time.Millisecond},
		Action{Type: ActionType("fake_act")})
	pc, err := s.Enqueue(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	child := NewJob("c", Owner{Kind: OwnerUser, ID: "u"},
		Schedule{Kind: ScheduleRelative, Relative: 50 * time.Millisecond},
		Action{Type: ActionType("fake_act")})
	child.DependsOn = []JobID{pc.ID}
	cc, err := s.Enqueue(ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	// Child should be blocked initially.
	q, _ := s.Query(cc.ID)
	if q.Status != StatusBlocked {
		t.Fatalf("child status initial: %s (want blocked)", q.Status)
	}
	// Wait for both to complete.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pq, _ := s.Query(pc.ID)
		cq, _ := s.Query(cc.ID)
		if pq.Status == StatusCompleted && cq.Status == StatusCompleted {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("dag did not finish — calls=%d", fa.calls.Load())
}
