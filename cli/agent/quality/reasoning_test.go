/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package quality

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/llm/client"
)

// agentWithType lets tests pin AgentType + Effort without dragging in a
// real worker. Returns nil/empty for everything else.
type agentWithType struct {
	t      workers.AgentType
	effort string
}

func (a *agentWithType) Type() workers.AgentType   { return a.t }
func (a *agentWithType) Name() string              { return string(a.t) }
func (a *agentWithType) Description() string       { return "" }
func (a *agentWithType) SystemPrompt() string      { return "" }
func (a *agentWithType) Skills() *workers.SkillSet { return nil }
func (a *agentWithType) AllowedCommands() []string { return nil }
func (a *agentWithType) IsReadOnly() bool          { return true }
func (a *agentWithType) Model() string             { return "" }
func (a *agentWithType) Effort() string            { return a.effort }
func (a *agentWithType) Execute(_ context.Context, _ string, _ *workers.WorkerDeps) (*workers.AgentResult, error) {
	return &workers.AgentResult{}, nil
}

func TestApplyAutoReasoning_OffMode(t *testing.T) {
	cfg := ReasoningConfig{Mode: "off", Budget: 8000, AutoAgents: []string{"planner"}}
	ctx := applyAutoReasoning(context.Background(), cfg, &agentWithType{t: "planner"})
	if e := client.EffortFromContext(ctx); e != client.EffortUnset {
		t.Errorf("off mode must not attach effort; got %q", e)
	}
}

func TestApplyAutoReasoning_AutoModeAttachesForListedAgent(t *testing.T) {
	cfg := ReasoningConfig{Mode: "auto", Budget: 8192, AutoAgents: []string{"planner", "verifier"}}
	ctx := applyAutoReasoning(context.Background(), cfg, &agentWithType{t: "planner"})
	if e := client.EffortFromContext(ctx); e != client.EffortHigh {
		t.Errorf("auto mode + listed agent should attach EffortHigh; got %q", e)
	}
}

func TestApplyAutoReasoning_AutoModeSkipsUnlistedAgent(t *testing.T) {
	cfg := ReasoningConfig{Mode: "auto", Budget: 8192, AutoAgents: []string{"planner"}}
	ctx := applyAutoReasoning(context.Background(), cfg, &agentWithType{t: "shell"})
	if e := client.EffortFromContext(ctx); e != client.EffortUnset {
		t.Errorf("auto mode + unlisted agent must not attach; got %q", e)
	}
}

func TestApplyAutoReasoning_OnModeAttachesForEveryAgent(t *testing.T) {
	cfg := ReasoningConfig{Mode: "on", Budget: 4096, AutoAgents: nil}
	ctx := applyAutoReasoning(context.Background(), cfg, &agentWithType{t: "shell"})
	if e := client.EffortFromContext(ctx); e != client.EffortMedium {
		t.Errorf("on mode must attach for any agent; got %q", e)
	}
}

func TestApplyAutoReasoning_PreservesExistingHint(t *testing.T) {
	cfg := ReasoningConfig{Mode: "on", Budget: 16384, AutoAgents: nil}
	base := client.WithEffortHint(context.Background(), client.EffortLow)
	ctx := applyAutoReasoning(base, cfg, &agentWithType{t: "shell"})
	if e := client.EffortFromContext(ctx); e != client.EffortLow {
		t.Errorf("existing hint must be preserved; got %q", e)
	}
}

func TestEffortForBudget(t *testing.T) {
	cases := []struct {
		budget int
		want   client.SkillEffort
	}{
		{0, client.EffortHigh},   // sane default
		{1000, client.EffortHigh},
		{4096, client.EffortMedium},
		{8192, client.EffortHigh},
		{16384, client.EffortMax},
		{32768, client.EffortMax},
	}
	for _, c := range cases {
		if got := EffortForBudget(c.budget); got != c.want {
			t.Errorf("EffortForBudget(%d) = %q, want %q", c.budget, got, c.want)
		}
	}
}

func TestInAutoAgents(t *testing.T) {
	if inAutoAgents("planner", nil) {
		t.Error("nil list must return false")
	}
	if inAutoAgents("planner", []string{}) {
		t.Error("empty list must return false")
	}
	if !inAutoAgents("PLANNER", []string{"planner"}) {
		t.Error("must be case-insensitive")
	}
	if !inAutoAgents("planner", []string{" PLANNER "}) {
		t.Error("must trim whitespace")
	}
	if inAutoAgents("planner", []string{"verifier", "refiner"}) {
		t.Error("absent agent must return false")
	}
}

func TestPipeline_ReasoningAutoFlowsThroughExecute(t *testing.T) {
	cfg := Defaults()
	cfg.Reasoning.Mode = "auto"
	cfg.Reasoning.AutoAgents = []string{"planner"}
	cfg.Reasoning.Budget = 8192

	captured := client.EffortUnset
	a := &captureCtxAgent{
		t:        "planner",
		captured: &captured,
	}
	p := New(cfg, nil)
	if _, err := p.Run(context.Background(), a, "x", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured != client.EffortHigh {
		t.Errorf("agent should receive ctx with EffortHigh; got %q", captured)
	}
}

func TestPipeline_ReasoningOffDoesNotInject(t *testing.T) {
	cfg := Defaults()
	cfg.Reasoning.Mode = "off"
	cfg.Reasoning.AutoAgents = []string{"planner"}

	captured := client.EffortUnset
	a := &captureCtxAgent{t: "planner", captured: &captured}
	p := New(cfg, nil)
	if _, err := p.Run(context.Background(), a, "x", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured != client.EffortUnset {
		t.Errorf("off mode must not inject effort; got %q", captured)
	}
}

// captureCtxAgent records the EffortFromContext value seen by Execute so
// the test can assert ctx propagation without poking at internals.
type captureCtxAgent struct {
	t        workers.AgentType
	captured *client.SkillEffort
}

func (a *captureCtxAgent) Type() workers.AgentType   { return a.t }
func (a *captureCtxAgent) Name() string              { return string(a.t) }
func (a *captureCtxAgent) Description() string       { return "" }
func (a *captureCtxAgent) SystemPrompt() string      { return "" }
func (a *captureCtxAgent) Skills() *workers.SkillSet { return nil }
func (a *captureCtxAgent) AllowedCommands() []string { return nil }
func (a *captureCtxAgent) IsReadOnly() bool          { return true }
func (a *captureCtxAgent) Model() string             { return "" }
func (a *captureCtxAgent) Effort() string            { return "" }
func (a *captureCtxAgent) Execute(ctx context.Context, _ string, _ *workers.WorkerDeps) (*workers.AgentResult, error) {
	if a.captured != nil {
		*a.captured = client.EffortFromContext(ctx)
	}
	return &workers.AgentResult{}, nil
}
