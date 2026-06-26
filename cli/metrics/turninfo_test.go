/*
 * ChatCLI - tests for per-turn telemetry rendering
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package metrics

import (
	"strings"
	"testing"
	"time"
)

// FormatTurnInfo must append pre-formatted telemetry parts (tokens · ctx · cost)
// to the turn line so agent/coder mode surfaces the same awareness chat shows.
func TestFormatTurnInfo_AppendsTelemetry(t *testing.T) {
	out := FormatTurnInfo(2, 10, 3*time.Second, &TurnStats{
		Telemetry: "1000↑ 500↓ · $0.0123 · ctx 12%",
	})
	for _, want := range []string{"Turn 2/10", "1000↑ 500↓", "$0.0123", "ctx 12%"} {
		if !strings.Contains(out, want) {
			t.Errorf("turn line %q missing %q", out, want)
		}
	}
}

// No telemetry → the line is unchanged from the counters-only baseline
// (non-regression: existing callers that pass no telemetry see no new text).
func TestFormatTurnInfo_NoTelemetryUnchanged(t *testing.T) {
	stats := &TurnStats{TurnToolCalls: 3}
	withNil := FormatTurnInfo(1, 5, time.Second, stats)
	if strings.Contains(withNil, "·") {
		t.Errorf("baseline line should not carry a telemetry separator: %q", withNil)
	}
	if !strings.Contains(withNil, "3 tool calls") {
		t.Errorf("counters still rendered: %q", withNil)
	}
}
