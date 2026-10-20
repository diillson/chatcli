/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"strings"
	"testing"
	"unicode/utf8"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// Log lines and agent observations are arbitrary UTF-8; the caps applied
// before they reach the LLM or the status must not split a character.
func TestTruncateLine_KeepsUTF8Valid(t *testing.T) {
	got := truncateLine(strings.Repeat("a", 6)+"日本語", 10)
	if !utf8.ValidString(got) || got != "aaaaaa..." {
		t.Fatalf("truncateLine = %q", got)
	}
}

func TestIsConverged_ReasonKeepsUTF8Valid(t *testing.T) {
	obs := strings.Repeat("a", 79) + "é" + strings.Repeat("b", 20)
	steps := []platformv1alpha1.AgenticStep{{Observation: obs}, {Observation: obs}, {Observation: obs}}
	converged, reason := (&ConvergenceDetector{}).IsConverged(steps)
	if !converged || !utf8.ValidString(reason) || strings.Contains(reason, `\x`) {
		t.Fatalf("converged=%v reason=%q", converged, reason)
	}
}
