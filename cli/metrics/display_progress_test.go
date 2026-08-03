/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package metrics

import (
	"strings"
	"testing"
)

// TestDispatchProgressBarEarnsFractionalCredit pins the live-bar fix: the
// percentage must advance while specialists are mid-flight (fraction of
// their turn budget), not only when an agent finishes.
func TestDispatchProgressBarEarnsFractionalCredit(t *testing.T) {
	slots := []struct{ CallID, Agent, Task string }{
		{"ac-1", "coder", "implement"},
		{"ac-2", "tester", "verify"},
	}
	state := NewAgentProgressState(2, slots)
	state.MarkStarted("ac-1")
	state.MarkStarted("ac-2")

	// No turn info yet — 0%.
	if out := FormatDispatchProgress(state, "m"); !strings.Contains(out, "(0%)") {
		t.Errorf("no live info must render 0%%, got header: %q", strings.SplitN(out, "\n", 2)[0])
	}

	// ac-1 at turn 16/30 → (15/30)/2 = 25%.
	state.SetLive("ac-1", 16, 30, "patch foo.go", nil)
	if out := FormatDispatchProgress(state, "m"); !strings.Contains(out, "(25%)") {
		t.Errorf("running agent must earn fractional credit, got header: %q", strings.SplitN(out, "\n", 2)[0])
	}

	// ac-2 completes → (1 + 15/30)/2 = 75%.
	state.MarkCompleted("ac-2", 0)
	if out := FormatDispatchProgress(state, "m"); !strings.Contains(out, "(75%)") {
		t.Errorf("completed + fractional must combine, got header: %q", strings.SplitN(out, "\n", 2)[0])
	}

	// A running agent can never render as 100%: last turn caps at 95%.
	soloState := NewAgentProgressState(1, slots[:1])
	soloState.MarkStarted("ac-1")
	soloState.SetLive("ac-1", 31, 30, "", nil)
	if out := FormatDispatchProgress(soloState, "m"); !strings.Contains(out, "(95%)") {
		t.Errorf("live fraction must cap below 100%%, got header: %q", strings.SplitN(out, "\n", 2)[0])
	}
}
