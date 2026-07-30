/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package agentevents

import "testing"

func TestToolKindNormalize(t *testing.T) {
	for _, k := range []ToolKind{KindRead, KindEdit, KindDelete, KindMove, KindSearch, KindExecute, KindThink, KindFetch, KindOther} {
		if got := k.Normalize(); got != k {
			t.Errorf("valid kind %q must pass through, got %q", k, got)
		}
	}
	for _, k := range []ToolKind{"", "banana", "READ", "exec"} {
		if got := k.Normalize(); got != KindOther {
			t.Errorf("invalid kind %q must clamp to other, got %q", k, got)
		}
	}
}

func TestToolStatusNormalize(t *testing.T) {
	for _, s := range []ToolStatus{StatusPending, StatusInProgress, StatusCompleted, StatusFailed} {
		if got := s.Normalize(); got != s {
			t.Errorf("valid status %q must pass through, got %q", s, got)
		}
	}
	for _, s := range []ToolStatus{"", "done", "IN_PROGRESS"} {
		if got := s.Normalize(); got != StatusPending {
			t.Errorf("invalid status %q must clamp to pending, got %q", s, got)
		}
	}
}

func TestToolStatusTerminal(t *testing.T) {
	terminal := map[ToolStatus]bool{
		StatusPending: false, StatusInProgress: false,
		StatusCompleted: true, StatusFailed: true,
	}
	for s, want := range terminal {
		if got := s.Terminal(); got != want {
			t.Errorf("Terminal(%q) = %v, want %v", s, got, want)
		}
	}
}
