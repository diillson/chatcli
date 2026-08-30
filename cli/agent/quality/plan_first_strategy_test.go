/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package quality

import (
	"os"
	"testing"
)

func TestPlanFirstStrategyDefaultAndEnv(t *testing.T) {
	if got := Defaults().PlanFirst.Strategy; got != PlanStrategyTaskGraph {
		t.Fatalf("default strategy must be taskgraph, got %q", got)
	}

	c := PlanFirstConfig{Strategy: PlanStrategyTaskGraph}
	t.Setenv("CHATCLI_QUALITY_PLAN_FIRST_STRATEGY", "plan-solve")
	loadPlanFirstEnv(&c)
	if c.Strategy != PlanStrategyPlanSolve {
		t.Fatalf("env override failed: %q", c.Strategy)
	}

	// Invalid value keeps the current strategy (normalizeMode fallback).
	os.Unsetenv("CHATCLI_QUALITY_PLAN_FIRST_STRATEGY")
	c2 := PlanFirstConfig{Strategy: PlanStrategyTaskGraph}
	t.Setenv("CHATCLI_QUALITY_PLAN_FIRST_STRATEGY", "bogus")
	loadPlanFirstEnv(&c2)
	if c2.Strategy != PlanStrategyTaskGraph {
		t.Fatalf("invalid env must keep current, got %q", c2.Strategy)
	}
}
