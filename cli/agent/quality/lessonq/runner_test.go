package lessonq

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/quality"
)

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func newTestRunner(t *testing.T, cfg *RunnerConfig) *Runner {
	t.Helper()
	c := DefaultRunnerConfig()
	c.BaseDir = t.TempDir()
	c.Workers = 1
	c.PerJobTimeout = 2 * time.Second
	if cfg != nil {
		if cfg.Workers > 0 {
			c.Workers = cfg.Workers
		}
		if cfg.QueueCapacity > 0 {
			c.QueueCapacity = cfg.QueueCapacity
		}
		if cfg.Retry.MaxAttempts > 0 {
			c.Retry = cfg.Retry
		}
		if cfg.PerJobTimeout > 0 {
			c.PerJobTimeout = cfg.PerJobTimeout
		}
		if cfg.StaleAfter != 0 {
			c.StaleAfter = cfg.StaleAfter
		}
	}
	r, err := NewRunner(c, nil)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r
}

func sampleRequest(task string) quality.LessonRequest {
	return quality.LessonRequest{
		Task: task, Attempt: "attempt", Outcome: "ERROR: x", Trigger: "error",
	}
}

func TestRunner_SuccessfulProcessing(t *testing.T) {
	r := newTestRunner(t, nil)
	defer r.DrainAndShutdown(time.Second)

	var processed atomic.Int32
	proc := func(_ context.Context, _ LessonJob) ProcessResult {
		processed.Add(1)
		return ProcessResult{Outcome: OutcomeSuccess}
	}
	if err := r.Start(context.Background(), proc); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := r.Enqueue(context.Background(), sampleRequest("t1")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitUntil(t, 2*time.Second, func() bool { return processed.Load() == 1 })
}

func TestRunner_EnqueueIsIdempotent(t *testing.T) {
	r := newTestRunner(t, nil)
	defer r.DrainAndShutdown(time.Second)

	block := make(chan struct{})
	var processed atomic.Int32
	proc := func(_ context.Context, _ LessonJob) ProcessResult {
		<-block
		processed.Add(1)
		return ProcessResult{Outcome: OutcomeSuccess}
	}
	_ = r.Start(context.Background(), proc)

	req := sampleRequest("same-task")
	for i := 0; i < 5; i++ {
		if err := r.Enqueue(context.Background(), req); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}
	close(block)
	time.Sleep(300 * time.Millisecond)
	if processed.Load() != 1 {
		t.Fatalf("duplicate enqueues must coalesce; processed=%d", processed.Load())
	}
}

func TestRunner_TransientRetriesThenSucceeds(t *testing.T) {
	cfg := DefaultRunnerConfig()
	cfg.Retry.InitialDelay = 20 * time.Millisecond
	cfg.Retry.MaxDelay = 50 * time.Millisecond
	cfg.Retry.Multiplier = 1.5
	cfg.Retry.JitterFraction = 0
	cfg.Retry.MaxAttempts = 5
	r := newTestRunner(t, &RunnerConfig{Retry: cfg.Retry})
	defer r.DrainAndShutdown(time.Second)

	var attempts atomic.Int32
	proc := func(_ context.Context, _ LessonJob) ProcessResult {
		n := attempts.Add(1)
		if n < 3 {
			return ProcessResult{Outcome: OutcomeTransient, Err: errors.New("boom")}
		}
		return ProcessResult{Outcome: OutcomeSuccess}
	}
	_ = r.Start(context.Background(), proc)
	_ = r.Enqueue(context.Background(), sampleRequest("retry-me"))
	waitUntil(t, 2*time.Second, func() bool { return attempts.Load() >= 3 })
}

func TestRunner_PermanentGoesToDLQ(t *testing.T) {
	r := newTestRunner(t, nil)
	defer r.DrainAndShutdown(time.Second)

	proc := func(_ context.Context, _ LessonJob) ProcessResult {
		return ProcessResult{Outcome: OutcomePermanent, Err: errors.New("parser")}
	}
	_ = r.Start(context.Background(), proc)
	_ = r.Enqueue(context.Background(), sampleRequest("bad"))

	waitUntil(t, 2*time.Second, func() bool { return r.DLQCount() == 1 })

	dlq, err := r.DLQList()
	if err != nil {
		t.Fatalf("DLQList: %v", err)
	}
	if len(dlq) != 1 || dlq[0].LastError == "" {
		t.Fatalf("DLQ entry missing error: %+v", dlq)
	}
}

func TestRunner_TransientExhaustionMovesToDLQ(t *testing.T) {
	r := newTestRunner(t, &RunnerConfig{
		Retry: RetryPolicy{
			InitialDelay:   10 * time.Millisecond,
			MaxDelay:       10 * time.Millisecond,
			Multiplier:     1.0,
			JitterFraction: 0,
			MaxAttempts:    3,
		},
	})
	defer r.DrainAndShutdown(time.Second)

	proc := func(_ context.Context, _ LessonJob) ProcessResult {
		return ProcessResult{Outcome: OutcomeTransient, Err: errors.New("always")}
	}
	_ = r.Start(context.Background(), proc)
	_ = r.Enqueue(context.Background(), sampleRequest("retry-exhaust"))

	waitUntil(t, 3*time.Second, func() bool { return r.DLQCount() == 1 })
}

func TestRunner_DLQReplay(t *testing.T) {
	r := newTestRunner(t, nil)
	defer r.DrainAndShutdown(time.Second)

	var mu sync.Mutex
	var mode string // "perm" until replayed, then "ok"
	mode = "perm"

	proc := func(_ context.Context, _ LessonJob) ProcessResult {
		mu.Lock()
		cur := mode
		mu.Unlock()
		if cur == "perm" {
			return ProcessResult{Outcome: OutcomePermanent, Err: errors.New("p")}
		}
		return ProcessResult{Outcome: OutcomeSuccess}
	}
	_ = r.Start(context.Background(), proc)
	_ = r.Enqueue(context.Background(), sampleRequest("replay-me"))
	waitUntil(t, 2*time.Second, func() bool { return r.DLQCount() == 1 })

	// Switch the processor mode, then replay the single DLQ entry.
	mu.Lock()
	mode = "ok"
	mu.Unlock()

	list, _ := r.DLQList()
	if len(list) != 1 {
		t.Fatalf("DLQ size should be 1; got %d", len(list))
	}
	id := list[0].ID
	if err := r.DLQReplay(context.Background(), id); err != nil {
		t.Fatalf("DLQReplay: %v", err)
	}
	waitUntil(t, 2*time.Second, func() bool { return r.DLQCount() == 0 })
}

func TestRunner_BootReplayRestoresWAL(t *testing.T) {
	// First runner: Enqueue two jobs, then shut it down with workers
	// that never processed anything (no processor started).
	base := t.TempDir()
	cfg := DefaultRunnerConfig()
	cfg.BaseDir = base
	cfg.Workers = 1

	r1, err := NewRunner(cfg, nil)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	// Don't Start — we want WAL to persist but nothing processed.
	_ = r1.Enqueue(context.Background(), sampleRequest("alpha"))
	_ = r1.Enqueue(context.Background(), sampleRequest("beta"))
	r1.DrainAndShutdown(time.Second)

	// Second runner: same BaseDir, Replay should pick up both jobs.
	r2, err := NewRunner(cfg, nil)
	if err != nil {
		t.Fatalf("NewRunner #2: %v", err)
	}
	defer r2.DrainAndShutdown(time.Second)

	var processed atomic.Int32
	proc := func(_ context.Context, _ LessonJob) ProcessResult {
		processed.Add(1)
		return ProcessResult{Outcome: OutcomeSuccess}
	}
	_ = r2.Start(context.Background(), proc)

	n, err := r2.Replay(context.Background())
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 replayed jobs; got %d", n)
	}
	waitUntil(t, 2*time.Second, func() bool { return processed.Load() == 2 })
}

func TestRunner_StaleReplayIsDiscarded(t *testing.T) {
	base := t.TempDir()
	cfg := DefaultRunnerConfig()
	cfg.BaseDir = base
	cfg.Workers = 1
	cfg.StaleAfter = time.Nanosecond // anything older than "right now" is stale

	r1, err := NewRunner(cfg, nil)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	_ = r1.Enqueue(context.Background(), sampleRequest("going-stale"))
	// Wait so the record's age > StaleAfter.
	time.Sleep(10 * time.Millisecond)
	r1.DrainAndShutdown(time.Second)

	r2, err := NewRunner(cfg, nil)
	if err != nil {
		t.Fatalf("NewRunner #2: %v", err)
	}
	defer r2.DrainAndShutdown(time.Second)
	var processed atomic.Int32
	proc := func(_ context.Context, _ LessonJob) ProcessResult {
		processed.Add(1)
		return ProcessResult{Outcome: OutcomeSuccess}
	}
	_ = r2.Start(context.Background(), proc)
	n, _ := r2.Replay(context.Background())
	if n != 0 {
		t.Fatalf("stale replay should discard all; got %d", n)
	}
	time.Sleep(100 * time.Millisecond)
	if processed.Load() != 0 {
		t.Fatalf("stale jobs must not be processed; processed=%d", processed.Load())
	}
}

func TestRunner_ProcessorPanicIsContained(t *testing.T) {
	r := newTestRunner(t, nil)
	defer r.DrainAndShutdown(time.Second)

	var called atomic.Int32
	proc := func(_ context.Context, j LessonJob) ProcessResult {
		called.Add(1)
		if j.Request.Task == "panicky" {
			panic("kaboom")
		}
		return ProcessResult{Outcome: OutcomeSuccess}
	}
	_ = r.Start(context.Background(), proc)
	_ = r.Enqueue(context.Background(), sampleRequest("panicky"))
	_ = r.Enqueue(context.Background(), sampleRequest("fine"))

	// Panicky should land in DLQ (classified as permanent). Fine
	// should still succeed.
	waitUntil(t, 2*time.Second, func() bool {
		return r.DLQCount() == 1 && called.Load() >= 2
	})
}

func TestRunner_GracefulShutdownTimeout(t *testing.T) {
	r := newTestRunner(t, nil)

	block := make(chan struct{})
	defer close(block)

	proc := func(_ context.Context, _ LessonJob) ProcessResult {
		<-block
		return ProcessResult{Outcome: OutcomeSuccess}
	}
	_ = r.Start(context.Background(), proc)
	_ = r.Enqueue(context.Background(), sampleRequest("blocks"))

	// Give worker a moment to pick up the job.
	time.Sleep(50 * time.Millisecond)
	// Shutdown must not hang beyond the timeout even with an in-flight
	// blocking processor.
	start := time.Now()
	r.DrainAndShutdown(150 * time.Millisecond)
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("shutdown should honor timeout; elapsed %s", elapsed)
	}
}

func TestRunner_BaseDirRequired(t *testing.T) {
	_, err := NewRunner(RunnerConfig{}, nil)
	if err == nil {
		t.Fatal("empty BaseDir must error")
	}
}

func TestRunner_EnqueueDurableOnWALBeforeQueue(t *testing.T) {
	// Use capacity=0 so the queue rejects synchronously, forcing us
	// to verify that WAL still got the record (for boot recovery).
	base := t.TempDir()
	cfg := DefaultRunnerConfig()
	cfg.BaseDir = base
	cfg.Workers = 0 // no workers, so Enqueue never drains
	cfg.QueueCapacity = 1
	cfg.EnqueueBlockTimeout = 50 * time.Millisecond

	r, err := NewRunner(cfg, nil)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	defer r.DrainAndShutdown(time.Second)

	// First accepts (fills cap).
	if err := r.Enqueue(context.Background(), sampleRequest("a")); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	// Second: queue rejects with full, but WAL should still have it.
	err = r.Enqueue(context.Background(), sampleRequest("b"))
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull on second; got %v", err)
	}
	// Verify both records landed in WAL — boot replay will pick them
	// both up on the next session.
	if cnt := r.wal.Count(); cnt != 2 {
		t.Fatalf("WAL should retain both records for recovery; got %d", cnt)
	}
}
