/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package pulse

import "testing"

func TestPatternSpanReportsItsOutcomeAsState(t *testing.T) {
	var none *Span
	none.Outcome(StatusOK, "nothing") // nil span: no-op

	bus := Default()
	ch, cancel := bus.Subscribe(16)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })

	BeginPattern(PatternSelfRefine, "run-7").Outcome(StatusOK, "rewrote draft")
	var got []Event
	for len(got) < 2 {
		if ev := recv(t, ch); ev.Kind == KindPattern {
			got = append(got, ev)
		}
	}
	if got[0].Name != PatternSelfRefine || got[0].Parent != "run-7" || got[0].Phase != PhaseStart {
		t.Fatalf("start = %+v", got[0])
	}
	if got[1].Attrs["state"] != "rewrote draft" || got[1].Status != StatusOK || got[1].Phase != PhaseEnd {
		t.Fatalf("end = %+v", got[1])
	}
}

// The dashboard promises seven patterns; a rename or a removal here must be
// a deliberate act.
func TestSevenPatternNamesAreDistinct(t *testing.T) {
	names := []string{PatternReAct, PatternPlanAndSolve, PatternReflexion, PatternRAGHyDE, PatternSelfRefine, PatternCoVe, PatternReasoning}
	seen := map[string]bool{}
	for _, n := range names {
		if n == "" || seen[n] {
			t.Fatalf("pattern name %q is empty or duplicated", n)
		}
		seen[n] = true
	}
	if len(seen) != 7 {
		t.Fatalf("patterns = %d, want 7", len(seen))
	}
}
