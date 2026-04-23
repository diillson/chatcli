package quality

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/workers"
)

// ─── state machine ─────────────────────────────────────────────────────────

func TestPipeline_InitialStateIsActive(t *testing.T) {
	p := New(Defaults(), nil)
	if p.State() != StateActive {
		t.Fatalf("want Active; got %s", p.State())
	}
}

func TestPipeline_DrainAndClose_Transitions(t *testing.T) {
	p := New(Defaults(), nil)
	p.DrainAndClose(10 * time.Millisecond)
	if p.State() != StateClosed {
		t.Fatalf("want Closed after DrainAndClose; got %s", p.State())
	}
}

func TestPipeline_ClosedBypassesHooks(t *testing.T) {
	p := New(Defaults(), nil).AddPost(&outputRewriter{name: "post", newOutput: "REWRITTEN"})
	p.DrainAndClose(10 * time.Millisecond)

	a := &fakeAgent{output: "direct"}
	res, err := p.Run(context.Background(), a, "t", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Output != "direct" {
		t.Fatalf("closed pipeline must bypass hooks; got %q", res.Output)
	}
}

func TestPipeline_DrainWaitsForInFlight(t *testing.T) {
	p := New(Defaults(), nil)

	block := make(chan struct{})
	defer close(block)
	slow := &slowAgent{block: block}

	done := make(chan struct{})
	go func() {
		_, _ = p.Run(context.Background(), slow, "t", nil)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond) // let Run latch in-flight counter

	drainDone := make(chan struct{})
	go func() {
		p.DrainAndClose(50 * time.Millisecond)
		close(drainDone)
	}()

	select {
	case <-drainDone:
		// Drain should timeout without completing — we never unblocked
		// the in-flight Run. After timeout it moves to Closed anyway.
	case <-time.After(200 * time.Millisecond):
		t.Fatal("DrainAndClose blocked past timeout")
	}
	if p.State() != StateClosed {
		t.Fatalf("expected Closed after drain timeout; got %s", p.State())
	}
}

// ─── COW snapshots ─────────────────────────────────────────────────────────

func TestPipeline_AddPreIncrementsGeneration(t *testing.T) {
	p := New(Defaults(), nil)
	g0 := p.Generation()
	p.AddPre(&taskRewriter{name: "r", prefix: "X"})
	if p.Generation() != g0+1 {
		t.Fatalf("generation should increment on AddPre; g0=%d now=%d", g0, p.Generation())
	}
	p.AddPost(&outputRewriter{name: "o", newOutput: "Y"})
	if p.Generation() != g0+2 {
		t.Fatalf("generation should increment on AddPost; now=%d want %d", p.Generation(), g0+2)
	}
}

func TestPipeline_SwapConfigIncrementsGeneration(t *testing.T) {
	p := New(Defaults(), nil)
	g0 := p.Generation()
	cfg := Defaults()
	cfg.Enabled = false
	p.SwapConfig(cfg)
	if p.Generation() != g0+1 {
		t.Fatalf("SwapConfig must increment generation; g0=%d now=%d", g0, p.Generation())
	}
	if p.Config().Enabled {
		t.Fatal("SwapConfig did not apply")
	}
}

func TestPipeline_ConcurrentAddAndRun(t *testing.T) {
	// Race detector is the primary assertion here — with `-race`,
	// Go will flag any unsynchronized access. We also sanity-check
	// the final hook count.
	p := New(Defaults(), nil)

	const N = 100
	var wg sync.WaitGroup

	// Continuously Run from multiple goroutines while AddPre races.
	wg.Add(4)
	for i := 0; i < 4; i++ {
		go func() {
			defer wg.Done()
			a := &fakeAgent{output: "ok"}
			for j := 0; j < 50; j++ {
				_, _ = p.Run(context.Background(), a, "t", nil)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			p.AddPre(&taskRewriter{name: fmt.Sprintf("r%d", i), prefix: ""})
		}
	}()
	wg.Wait()

	pre, _ := p.HookCounts()
	if pre != N {
		t.Fatalf("expected %d pre-hooks; got %d", N, pre)
	}
}

// ─── priority ordering ─────────────────────────────────────────────────────

type priorityPre struct {
	name     string
	priority int
	prefix   string
}

func (p *priorityPre) Name() string  { return p.name }
func (p *priorityPre) Priority() int { return p.priority }
func (p *priorityPre) PreRun(_ context.Context, hc *HookContext) (string, error) {
	return p.prefix + hc.Task, nil
}

func TestPipeline_PriorityOrdersPreHooks(t *testing.T) {
	// Register a low-priority hook first, then a high-priority one.
	// Priority-ordering should run the high-priority one first.
	p := New(Defaults(), nil).
		AddPre(&priorityPre{name: "late", priority: 200, prefix: "[late]"}).
		AddPre(&priorityPre{name: "early", priority: 50, prefix: "[early]"})
	a := &fakeAgent{output: "ok"}
	_, _ = p.Run(context.Background(), a, "x", nil)
	// early runs first, prepends [early]; late runs on the result and
	// prepends [late]. Final task: "[late][early]x".
	if a.lastTask != "[late][early]x" {
		t.Fatalf("priority order wrong; got %q", a.lastTask)
	}
}

// ─── short-circuit sentinels ────────────────────────────────────────────────

type shortCircuitingPre struct{ name, output string }

func (s *shortCircuitingPre) Name() string { return s.name }
func (s *shortCircuitingPre) PreRun(_ context.Context, hc *HookContext) (string, error) {
	hc.SetShortCircuit(s.output)
	return "", ErrSkipExecution
}

func TestPipeline_ErrSkipExecution(t *testing.T) {
	post := &captureOutputPost{}
	p := New(Defaults(), nil).
		AddPre(&shortCircuitingPre{name: "cache", output: "from-cache"}).
		AddPost(post)
	a := &fakeAgent{output: "would-be-executed"}
	res, err := p.Run(context.Background(), a, "t", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if a.calls != 0 {
		t.Fatalf("agent.Execute should be skipped; calls=%d", a.calls)
	}
	if res.Output != "from-cache" {
		t.Fatalf("output should come from short-circuit; got %q", res.Output)
	}
	if post.seen != "from-cache" {
		t.Fatalf("post-hook should see short-circuit output; got %q", post.seen)
	}
}

type failingSkipRemainingPre struct{ name string }

func (f *failingSkipRemainingPre) Name() string { return f.name }
func (f *failingSkipRemainingPre) PreRun(_ context.Context, _ *HookContext) (string, error) {
	return "", ErrSkipRemainingHooks
}

func TestPipeline_ErrSkipRemainingHooksPre(t *testing.T) {
	second := &taskRewriter{name: "second", prefix: "SHOULD_NOT_RUN:"}
	p := New(Defaults(), nil).
		AddPre(&failingSkipRemainingPre{name: "first"}).
		AddPre(second)
	a := &fakeAgent{output: "ok"}
	_, err := p.Run(context.Background(), a, "x", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if a.calls != 1 {
		t.Fatalf("agent.Execute should still run; calls=%d", a.calls)
	}
	if a.lastTask != "x" {
		t.Fatalf("second pre-hook ran despite SkipRemaining; got %q", a.lastTask)
	}
}

type failingSkipRemainingPost struct{ name string }

func (f *failingSkipRemainingPost) Name() string { return f.name }
func (f *failingSkipRemainingPost) PostRun(_ context.Context, _ *HookContext, _ *workers.AgentResult) error {
	return ErrSkipRemainingHooks
}

func TestPipeline_ErrSkipRemainingHooksPost(t *testing.T) {
	second := &outputRewriter{name: "second", newOutput: "SHOULD_NOT_SEE"}
	p := New(Defaults(), nil).
		AddPost(&failingSkipRemainingPost{name: "first"}).
		AddPost(second)
	a := &fakeAgent{output: "draft"}
	res, err := p.Run(context.Background(), a, "x", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Output != "draft" {
		t.Fatalf("second post-hook ran despite SkipRemaining; output=%q", res.Output)
	}
}

// ─── helpers ───────────────────────────────────────────────────────────────

type slowAgent struct {
	fakeAgent
	block chan struct{}
}

func (s *slowAgent) Execute(ctx context.Context, task string, deps *workers.WorkerDeps) (*workers.AgentResult, error) {
	select {
	case <-s.block:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &workers.AgentResult{Output: "slow"}, nil
}

type captureOutputPost struct {
	seen string
}

func (c *captureOutputPost) Name() string { return "capture-post" }
func (c *captureOutputPost) PostRun(_ context.Context, _ *HookContext, r *workers.AgentResult) error {
	c.seen = r.Output
	return nil
}

// panickyPre is referenced by pipeline_isolation_test.go.
type panickyPre struct {
	name    string
	counter *atomic.Int32
}

func (p *panickyPre) Name() string { return p.name }
func (p *panickyPre) PreRun(_ context.Context, _ *HookContext) (string, error) {
	if p.counter != nil {
		p.counter.Add(1)
	}
	panic("boom from pre")
}

// hangingPre blocks until its ctx expires. Used in timeout tests.
type hangingPre struct{ name string }

func (h *hangingPre) Name() string { return h.name }
func (h *hangingPre) PreRun(ctx context.Context, _ *HookContext) (string, error) {
	<-ctx.Done()
	return "", errors.New("should not be observed by caller")
}
