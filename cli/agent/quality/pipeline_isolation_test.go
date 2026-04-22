package quality

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/workers"
)

// ─── panic containment ────────────────────────────────────────────────────

func TestPipeline_PreHookPanicIsolated(t *testing.T) {
	calls := &atomic.Int32{}
	later := &taskRewriter{name: "after-panic", prefix: "X:"}
	p := New(Defaults(), nil).
		AddPre(&panickyPre{name: "panic-pre", counter: calls}).
		AddPre(later)
	a := &fakeAgent{output: "ok"}

	res, err := p.Run(context.Background(), a, "t", nil)
	if err != nil {
		t.Fatalf("pipeline should absorb panic; got err=%v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("panicky hook should have been invoked once; got %d", calls.Load())
	}
	// later still ran because we skipped only the panicky hook.
	if a.lastTask != "X:t" {
		t.Fatalf("subsequent pre-hook should still run; got %q", a.lastTask)
	}
	if res.Output != "ok" {
		t.Fatalf("agent should still run; got %q", res.Output)
	}
}

type panickyPost struct{ name string }

func (p *panickyPost) Name() string { return p.name }
func (p *panickyPost) PostRun(_ context.Context, _ *HookContext, _ *workers.AgentResult) error {
	panic("boom from post")
}

func TestPipeline_PostHookPanicIsolated(t *testing.T) {
	later := &outputRewriter{name: "later-post", newOutput: "AFTER-PANIC"}
	p := New(Defaults(), nil).
		AddPost(&panickyPost{name: "panic-post"}).
		AddPost(later)
	a := &fakeAgent{output: "ok"}

	res, err := p.Run(context.Background(), a, "t", nil)
	if err != nil {
		t.Fatalf("pipeline should absorb panic; got err=%v", err)
	}
	if res.Output != "AFTER-PANIC" {
		t.Fatalf("later post-hook should still run; got %q", res.Output)
	}
}

// ─── timeout enforcement ──────────────────────────────────────────────────

func TestPipeline_PreHookTimeout(t *testing.T) {
	p := New(Defaults(), nil).AddPre(&hangingPre{name: "hang"})
	p.SetHookTimeout(50 * time.Millisecond)

	a := &fakeAgent{output: "ok"}
	start := time.Now()
	_, err := p.Run(context.Background(), a, "t", nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("hanging hook should time out quietly; got err=%v", err)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("hook timeout did not fire; elapsed=%s", elapsed)
	}
	if a.calls != 1 {
		t.Fatalf("agent should run after pre-hook times out; calls=%d", a.calls)
	}
}

type hangingPost struct{ name string }

func (h *hangingPost) Name() string { return h.name }
func (h *hangingPost) PostRun(ctx context.Context, _ *HookContext, _ *workers.AgentResult) error {
	<-ctx.Done()
	return nil
}

func TestPipeline_PostHookTimeout(t *testing.T) {
	p := New(Defaults(), nil).AddPost(&hangingPost{name: "hang-post"})
	p.SetHookTimeout(30 * time.Millisecond)
	a := &fakeAgent{output: "ok"}
	start := time.Now()
	_, err := p.Run(context.Background(), a, "t", nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("post-hook timeout did not fire; elapsed=%s", elapsed)
	}
}

// ─── circuit breaker ──────────────────────────────────────────────────────

type alwaysFailingPost struct {
	name  string
	calls *atomic.Int32
}

func (a *alwaysFailingPost) Name() string { return a.name }
func (a *alwaysFailingPost) PostRun(_ context.Context, _ *HookContext, _ *workers.AgentResult) error {
	if a.calls != nil {
		a.calls.Add(1)
	}
	return errors.New("always fails")
}

func TestPipeline_BreakerTripsAfterThreshold(t *testing.T) {
	calls := &atomic.Int32{}
	hook := &alwaysFailingPost{name: "failing", calls: calls}
	p := New(Defaults(), nil).AddPost(hook)
	a := &fakeAgent{output: "ok"}

	// Default threshold = 5. After 5 failures the breaker opens and
	// subsequent invocations are skipped — calls counter stops.
	for i := 0; i < 10; i++ {
		_, _ = p.Run(context.Background(), a, "t", nil)
	}
	got := calls.Load()
	if got < 5 {
		t.Fatalf("hook should have been invoked at least 5 times; got %d", got)
	}
	if got >= 10 {
		t.Fatalf("breaker did not trip: hook invoked %d times in 10 runs", got)
	}
}

// ─── concurrency + determinism ────────────────────────────────────────────

func TestPipeline_ConcurrentRunsAreIsolated(t *testing.T) {
	// Each Run should observe its own HookContext — this test uses a
	// pre-hook that mutates the task based on a goroutine-local ID so
	// we can verify no cross-talk.
	p := New(Defaults(), nil).AddPre(&idPrefixingPre{name: "prefix"})
	const N = 50
	errs := make(chan string, N)
	for i := 0; i < N; i++ {
		go func(i int) {
			a := &fakeAgent{output: "ok"}
			_, _ = p.Run(context.Background(), a, fmt.Sprintf("id=%d", i), nil)
			if a.lastTask != fmt.Sprintf("PFX:id=%d", i) {
				errs <- fmt.Sprintf("run %d: got %q", i, a.lastTask)
				return
			}
			errs <- ""
		}(i)
	}
	for i := 0; i < N; i++ {
		if msg := <-errs; msg != "" {
			t.Error(msg)
		}
	}
}

type idPrefixingPre struct{ name string }

func (i *idPrefixingPre) Name() string { return i.name }
func (i *idPrefixingPre) PreRun(_ context.Context, hc *HookContext) (string, error) {
	return "PFX:" + hc.Task, nil
}

// ─── nil-safety / defensive checks ────────────────────────────────────────

func TestPipeline_HookCountsHonorRegistrations(t *testing.T) {
	p := New(Defaults(), nil)
	if pre, post := p.HookCounts(); pre != 0 || post != 0 {
		t.Fatalf("initial counts should be 0; got %d/%d", pre, post)
	}
	p.AddPre(&taskRewriter{name: "p1"})
	p.AddPre(&taskRewriter{name: "p2"})
	p.AddPost(&outputRewriter{name: "o1"})
	if pre, post := p.HookCounts(); pre != 2 || post != 1 {
		t.Fatalf("counts wrong after adds; got %d/%d", pre, post)
	}
}
