package quality

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/cli/agent/quality/convergence"
	"github.com/diillson/chatcli/cli/agent/workers"
)

// stubChecker lets us script convergence decisions per-pass so we
// can test the RefineHook's regression guard + convergence short-
// circuit deterministically.
type stubChecker struct {
	// responses is consumed in order; if exhausted, returns
	// {Converged:false} forever.
	responses []convergence.CheckResult
	calls     int
}

func (s *stubChecker) Check(_ context.Context, _, _ string) (convergence.CheckResult, error) {
	if s.calls >= len(s.responses) {
		s.calls++
		return convergence.CheckResult{}, nil
	}
	r := s.responses[s.calls]
	s.calls++
	return r, nil
}

// dispatchFn is a tiny DispatchOne stub that returns canned rewrites.
// Each call pops from the rewrites queue.
func dispatchFn(rewrites []string) DispatchOne {
	i := 0
	return func(_ context.Context, _ workers.AgentCall) workers.AgentResult {
		if i >= len(rewrites) {
			return workers.AgentResult{Output: rewrites[len(rewrites)-1]}
		}
		out := rewrites[i]
		i++
		return workers.AgentResult{Output: out}
	}
}

func TestRefineHook_ConvergenceShortCircuit(t *testing.T) {
	// Pass 1: refiner rewrites to "v2". Checker says converged.
	// → Hook should stop after pass 1 and use "v2".
	cfg := Defaults()
	cfg.Refine.Enabled = true
	cfg.Refine.MaxPasses = 5
	cfg.Refine.MinDraftBytes = 1

	checker := &stubChecker{
		responses: []convergence.CheckResult{
			// Similarity to original (for regression guard) — high
			{Score: convergence.Score{Similarity: 0.98}, Converged: false},
			// Convergence check current vs next — converged!
			{Score: convergence.Score{Similarity: 0.98}, Converged: true},
		},
	}
	hook := NewRefineHookWithConvergence(dispatchFn([]string{"v2", "v3", "v4"}), checker, nil)

	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "t", Config: cfg}
	res := &workers.AgentResult{Output: "original draft that is long enough"}
	if err := hook.PostRun(context.Background(), hc, res); err != nil {
		t.Fatalf("PostRun: %v", err)
	}
	if res.Output != "v2" {
		t.Fatalf("expected convergence to lock in v2; got %q", res.Output)
	}
}

func TestRefineHook_RegressionDetectionRollsBack(t *testing.T) {
	// Pass 1: rewrite is high-sim to original (bestSim=0.95).
	// Pass 2: rewrite is low-sim to original (sim=0.5) → regression
	// detected → revert to v1 (bestDraft).
	cfg := Defaults()
	cfg.Refine.Enabled = true
	cfg.Refine.MaxPasses = 5
	cfg.Refine.MinDraftBytes = 1

	checker := &stubChecker{
		responses: []convergence.CheckResult{
			// Pass 1 sim-to-original: high (keeps as best)
			{Score: convergence.Score{Similarity: 0.95}},
			// Pass 1 current-vs-next convergence: not converged
			{Score: convergence.Score{Similarity: 0.7}, Converged: false},
			// Pass 2 sim-to-original: big drop → regression
			{Score: convergence.Score{Similarity: 0.4}},
		},
	}
	hook := NewRefineHookWithConvergence(
		dispatchFn([]string{"v1 is good", "v2 drifted"}),
		checker, nil)

	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "t", Config: cfg}
	res := &workers.AgentResult{Output: "original draft that is long enough"}
	_ = hook.PostRun(context.Background(), hc, res)

	if res.Output != "v1 is good" {
		t.Fatalf("regression should have rolled back to best draft v1; got %q", res.Output)
	}
	if !res.MetadataFlag("refine_rolled_back") {
		t.Fatal("refine_rolled_back metadata flag not set")
	}
}

func TestRefineHook_FallbackWhenCheckerNil(t *testing.T) {
	// With no checker, hook must still use the legacy char heuristic
	// and not crash.
	cfg := Defaults()
	cfg.Refine.Enabled = true
	cfg.Refine.MaxPasses = 1
	cfg.Refine.MinDraftBytes = 1

	hook := NewRefineHookWithConvergence(
		dispatchFn([]string{"nearly identical draft"}),
		nil, nil)
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "t", Config: cfg}
	res := &workers.AgentResult{Output: "nearly identical draft!"}
	if err := hook.PostRun(context.Background(), hc, res); err != nil {
		t.Fatalf("PostRun without checker: %v", err)
	}
	// Just validate we produced some output and didn't panic.
	if res.Output == "" {
		t.Fatal("output must not be empty")
	}
}
