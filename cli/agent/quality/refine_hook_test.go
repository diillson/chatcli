/*
 * ChatCLI - RefineHook tests (Phase 5).
 */
package quality

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent/workers"
)

// captureDispatch records every dispatch call and returns canned
// responses. The response template lets a test return different
// outputs per call (so multi-pass tests can simulate convergence).
type captureDispatch struct {
	calls    []workers.AgentCall
	response func(call workers.AgentCall, idx int) workers.AgentResult
}

func (c *captureDispatch) handle(_ context.Context, call workers.AgentCall) workers.AgentResult {
	idx := len(c.calls)
	c.calls = append(c.calls, call)
	return c.response(call, idx)
}

func TestRefineHook_DisabledIsNoop(t *testing.T) {
	cfg := Defaults()
	cfg.Refine.Enabled = false
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "x", Config: cfg}
	res := &workers.AgentResult{Output: strings.Repeat("x", 500)}

	cd := &captureDispatch{response: func(_ workers.AgentCall, _ int) workers.AgentResult {
		t.Fatal("dispatch should not run when disabled")
		return workers.AgentResult{}
	}}
	if err := NewRefineHook(cd.handle, nil).PostRun(context.Background(), hc, res); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cd.calls) != 0 {
		t.Errorf("expected no calls; got %d", len(cd.calls))
	}
}

func TestRefineHook_ShortDraftSkipped(t *testing.T) {
	cfg := Defaults()
	cfg.Refine.Enabled = true
	cfg.Refine.MinDraftBytes = 200
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "x", Config: cfg}
	res := &workers.AgentResult{Output: "too short"}

	cd := &captureDispatch{response: func(_ workers.AgentCall, _ int) workers.AgentResult {
		t.Fatal("short drafts must skip refine")
		return workers.AgentResult{}
	}}
	_ = NewRefineHook(cd.handle, nil).PostRun(context.Background(), hc, res)
}

func TestRefineHook_ExcludedAgentSkipped(t *testing.T) {
	cfg := Defaults()
	cfg.Refine.Enabled = true
	cfg.Refine.ExcludeAgents = []string{"formatter"}
	hc := &HookContext{Agent: &agentWithType{t: "formatter"}, Task: "x", Config: cfg}
	res := &workers.AgentResult{Output: strings.Repeat("x", 500)}

	cd := &captureDispatch{response: func(_ workers.AgentCall, _ int) workers.AgentResult {
		t.Fatal("excluded agent must skip refine")
		return workers.AgentResult{}
	}}
	_ = NewRefineHook(cd.handle, nil).PostRun(context.Background(), hc, res)
}

func TestRefineHook_AgentErrorSkipsRefine(t *testing.T) {
	cfg := Defaults()
	cfg.Refine.Enabled = true
	cfg.Refine.MinDraftBytes = 5
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "x", Config: cfg}
	res := &workers.AgentResult{Output: "anything", Error: errBoom}

	cd := &captureDispatch{response: func(_ workers.AgentCall, _ int) workers.AgentResult {
		t.Fatal("errored worker must skip refine")
		return workers.AgentResult{}
	}}
	_ = NewRefineHook(cd.handle, nil).PostRun(context.Background(), hc, res)
}

func TestRefineHook_RewritesOutput(t *testing.T) {
	cfg := Defaults()
	cfg.Refine.Enabled = true
	cfg.Refine.MinDraftBytes = 5
	cfg.Refine.MaxPasses = 1
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "ship a hello", Config: cfg}
	res := &workers.AgentResult{Output: "draft hello"}

	cd := &captureDispatch{response: func(call workers.AgentCall, _ int) workers.AgentResult {
		if call.Agent != workers.AgentTypeRefiner {
			t.Errorf("hook must dispatch to refiner; got %s", call.Agent)
		}
		if !strings.Contains(call.Task, workers.RefineDirective) {
			t.Errorf("dispatch task must carry RefineDirective")
		}
		return workers.AgentResult{Output: "polished hello"}
	}}
	if err := NewRefineHook(cd.handle, nil).PostRun(context.Background(), hc, res); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Output != "polished hello" {
		t.Errorf("output should be replaced; got %q", res.Output)
	}
}

func TestRefineHook_MultiPassConverges(t *testing.T) {
	cfg := Defaults()
	cfg.Refine.Enabled = true
	cfg.Refine.MinDraftBytes = 5
	cfg.Refine.MaxPasses = 5
	cfg.Refine.EpsilonChars = 5
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "task", Config: cfg}
	res := &workers.AgentResult{Output: "draft v0"}

	// Pass 0: change to "draft v1" (delta > epsilon → continue)
	// Pass 1: same as pass 0's output (delta == 0 → converge → stop)
	cd := &captureDispatch{response: func(_ workers.AgentCall, idx int) workers.AgentResult {
		if idx == 0 {
			return workers.AgentResult{Output: "rewrite v1 longer text here"}
		}
		return workers.AgentResult{Output: "rewrite v1 longer text here"}
	}}
	if err := NewRefineHook(cd.handle, nil).PostRun(context.Background(), hc, res); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cd.calls) > 2 {
		t.Errorf("convergence must short-circuit after 2 passes; got %d", len(cd.calls))
	}
}

func TestRefineHook_DispatchErrorKeepsOriginalDraft(t *testing.T) {
	cfg := Defaults()
	cfg.Refine.Enabled = true
	cfg.Refine.MinDraftBytes = 5
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "task", Config: cfg}
	res := &workers.AgentResult{Output: "original draft"}

	cd := &captureDispatch{response: func(_ workers.AgentCall, _ int) workers.AgentResult {
		return workers.AgentResult{Error: errBoom}
	}}
	_ = NewRefineHook(cd.handle, nil).PostRun(context.Background(), hc, res)
	if res.Output != "original draft" {
		t.Errorf("dispatch error must preserve draft; got %q", res.Output)
	}
}

func TestConvergedRefine(t *testing.T) {
	if !convergedRefine("hello world", "hello world", 5) {
		t.Error("identical strings must converge")
	}
	if convergedRefine("hello", "totally different and longer", 5) {
		t.Error("very different strings must not converge")
	}
	if !convergedRefine("hello", "helloo", 2) {
		t.Error("delta within epsilon must converge")
	}
}

var errBoom = errBoomT{}

type errBoomT struct{}

func (errBoomT) Error() string { return "boom" }
