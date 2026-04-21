/*
 * ChatCLI - Early-exit heuristics for the ReAct loop.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/cli/agent"
)

// stagnationTracker keeps a short sliding window of tool_call fingerprints
// and detects when the model is looping on the SAME set of calls turn after
// turn without new information — the classic "reflection loop" failure
// mode that makes trivial queries burn 20k+ tokens.
//
// The detection is intentionally conservative: we only break out after the
// same signature has been seen enough times to be statistically suspicious
// (DefaultStagnationThreshold, 3 consecutive identical turns by default).
// A single repeated call is fine — the model might just be re-trying after
// a transient failure.
type stagnationTracker struct {
	history   []string
	threshold int
	window    int
}

// stagnationExitEnv toggles the early-exit detector. Set to "0" or "false"
// to fully disable (falls back to the pre-existing MaxTurns-only loop).
const stagnationExitEnv = "CHATCLI_AGENT_EARLY_EXIT"

const (
	// DefaultStagnationThreshold is how many consecutive turns must share
	// the same tool_call fingerprint before we treat it as a stall.
	DefaultStagnationThreshold = 3
	// DefaultStagnationWindow caps the number of fingerprints we retain.
	DefaultStagnationWindow = 6
)

// earlyExitEnabled reports whether the stagnation detector is active for
// the current session. Honors `CHATCLI_AGENT_EARLY_EXIT=0|false`.
func earlyExitEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(stagnationExitEnv)))
	switch v {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// stagnationThreshold returns the configured threshold, honoring
// CHATCLI_AGENT_EARLY_EXIT_TURNS for power users. Clamped to [2, 10].
func stagnationThreshold() int {
	if v := strings.TrimSpace(os.Getenv("CHATCLI_AGENT_EARLY_EXIT_TURNS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 2 && n <= 10 {
			return n
		}
	}
	return DefaultStagnationThreshold
}

// newStagnationTracker builds a tracker with the configured threshold.
func newStagnationTracker() *stagnationTracker {
	t := stagnationThreshold()
	w := DefaultStagnationWindow
	if w < t {
		w = t
	}
	return &stagnationTracker{
		history:   make([]string, 0, w),
		threshold: t,
		window:    w,
	}
}

// toolCallFingerprint returns a stable hash for a batch of tool calls,
// order-independent but arg-sensitive. Two turns with the SAME calls and
// SAME args produce the same fingerprint regardless of ordering — this
// matches the "is the model asking for the same thing again?" intuition.
//
// An empty batch returns "" (no tool calls this turn — treated specially
// by Observe so we don't conflate "final answer" with "stalled").
func toolCallFingerprint(calls []agent.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	sigs := make([]string, 0, len(calls))
	for _, c := range calls {
		// Normalize args: collapse whitespace and lowercase to absorb
		// trivial formatting differences (the model re-emitting the
		// same call with an extra space shouldn't reset the counter).
		args := strings.Join(strings.Fields(c.Args), " ")
		sigs = append(sigs, c.Name+"|"+args)
	}
	sort.Strings(sigs)
	sum := sha256.Sum256([]byte(strings.Join(sigs, "\n")))
	return hex.EncodeToString(sum[:8])
}

// Observe records this turn's fingerprint and returns true if the
// detector thinks we are stalled. Empty fingerprints (no tool calls)
// reset the counter — they represent the model actually finishing.
func (t *stagnationTracker) Observe(fp string) (stalled bool, repeats int) {
	if fp == "" {
		// No tool calls — the existing loop already handles this as a
		// final answer / user-input wait. Reset so the next tool burst
		// starts fresh.
		t.history = t.history[:0]
		return false, 0
	}
	t.history = append(t.history, fp)
	if len(t.history) > t.window {
		t.history = t.history[len(t.history)-t.window:]
	}
	// Count trailing run of identical fingerprints.
	run := 1
	for i := len(t.history) - 2; i >= 0; i-- {
		if t.history[i] == fp {
			run++
		} else {
			break
		}
	}
	return run >= t.threshold, run
}
