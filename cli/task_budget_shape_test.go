/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/llm/client"
)

// TestAnthropicTaskBudgetFor_CeilingIsFixed pins what the field means: a
// total the run announced once, and a remainder that only shrinks. Sending
// the same number for both — which is what a per-turn recomputation
// produces — shows the model a ceiling that moves with its own spend.
func TestAnthropicTaskBudgetFor_CeilingIsFixed(t *testing.T) {
	const total = 400000
	b := client.AnthropicTaskBudgetFor(total, 120000)
	if b == nil || b.Total != total || b.Remaining != 120000 {
		t.Fatalf("mid-run budget = %+v, want a fixed ceiling with a smaller remainder", b)
	}

	// A daily limit rolling over at midnight makes the remaining spend
	// jump. The ceiling a run announced is the ceiling it keeps.
	if grown := client.AnthropicTaskBudgetFor(total, total*3); grown.Remaining != total {
		t.Fatalf("remaining must never exceed the announced ceiling, got %d", grown.Remaining)
	}

	// Nothing to say stays nothing to say, so callers assign without a check.
	if client.AnthropicTaskBudgetFor(0, 0) != nil || client.AnthropicTaskBudgetFor(total, 0) != nil {
		t.Fatal("an empty budget must be nil")
	}

	// The first turn of a run is the one case where both are equal.
	if first := client.AnthropicTaskBudget(total); first.Total != total || first.Remaining != total {
		t.Fatalf("first turn = %+v", first)
	}
}

// TestRemainingTaskBudgetTokensFor_PricesTheServingPair covers a session
// that switched models. The session-wide average blends prices that differ
// by an order of magnitude, so the ceiling it produces is denominated in a
// model that does not exist.
func TestRemainingTaskBudgetTokensFor_PricesTheServingPair(t *testing.T) {
	ct := NewCostTracker()
	ct.budgetLimitUSD = 100
	ct.budgetHardStop = true

	// An expensive model did most of the session's work, and a cheap one
	// is about to serve the next turn.
	seed := func(provider, model string, tokens int64, cost float64) {
		rec := ct.getOrCreateRecord(modelKey(provider, model), provider, model)
		rec.CompletionTokens = tokens
		rec.TotalCostUSD = cost
		ct.totalCompletionTokens += tokens
		ct.totalCostUSD += cost
	}
	seed("ANTHROPIC", "claude-opus-5", 100000, 7.5) // $75 / Mtok
	seed("OPENAI", "gpt-5.6-luna", 100000, 0.5)     // $5  / Mtok

	blended, ok := ct.RemainingTaskBudgetTokens()
	if !ok {
		t.Fatal("the session average must still work as a fallback")
	}
	cheap, ok := ct.RemainingTaskBudgetTokensFor("OPENAI", "gpt-5.6-luna")
	if !ok {
		t.Fatal("a pair with its own samples must be priced by them")
	}
	if cheap <= blended {
		t.Fatalf("the cheap model must buy more tokens than the blended average: %d vs %d", cheap, blended)
	}
	expensive, _ := ct.RemainingTaskBudgetTokensFor("ANTHROPIC", "claude-opus-5")
	if expensive >= blended {
		t.Fatalf("the expensive model must buy fewer: %d vs %d", expensive, blended)
	}

	// A pair with no samples of its own falls back rather than
	// extrapolating from nothing.
	fresh, ok := ct.RemainingTaskBudgetTokensFor("XAI", "grok-5")
	if !ok || fresh != blended {
		t.Fatalf("an unsampled pair must fall back to the session average, got %d ok=%v", fresh, ok)
	}
}

// TestAgentMode_TaskBudgetCeilingIsPerRun covers the state that outlived
// the run it belonged to. AgentMode is one instance for the whole
// session, so the ceiling fixed on the first run was still there on the
// second: a fresh task announced a total it had already spent most of,
// and the model wound down something that had barely started.
func TestAgentMode_TaskBudgetCeilingIsPerRun(t *testing.T) {
	a := &AgentMode{taskBudgetTotal: 400000}
	a.resetPerRunState()
	if a.taskBudgetTotal != 0 {
		t.Fatalf("a new run must announce its own ceiling, inherited %d", a.taskBudgetTotal)
	}
}
