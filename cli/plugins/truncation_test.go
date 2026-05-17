/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// truncatedPlugin is a configurable plugin whose MaxResultChars()
// returns a test-supplied value. Used to verify the helper plumbs
// the cap correctly through EffectiveMaxResultChars.
type truncatedPlugin struct {
	minimalPlugin
	cap int
}

func (p truncatedPlugin) MaxResultChars() int { return p.cap }

// TestTruncateForLLM_PassesShortStringsThrough is the happy path.
func TestTruncateForLLM_PassesShortStringsThrough(t *testing.T) {
	assert.Equal(t, "short", TruncateForLLM("short", 1000))
	assert.Equal(t, "", TruncateForLLM("", 1000))
}

// TestTruncateForLLM_TruncatesOversize pins the head+tail shape and
// the byte-count marker. We use a cap (8000) strictly larger than
// preview+suffix (6000) so the head/tail branch fires, not the
// "cap too small" fallback.
func TestTruncateForLLM_TruncatesOversize(t *testing.T) {
	body := strings.Repeat("a", 10_000) + strings.Repeat("b", 5000) + strings.Repeat("c", 1000)
	got := TruncateForLLM(body, 8000)
	assert.True(t, strings.Contains(got, "TRUNCATED"))
	// Head should contain only 'a's (the prefix).
	headSlice := got[:TruncationPreviewSize]
	assert.NotContains(t, headSlice, "b", "head must be the original prefix")
	// Tail should contain only 'c's (the suffix from the original).
	tailSlice := got[len(got)-TruncationSuffixSize:]
	assert.NotContains(t, tailSlice, "a", "tail must be the original suffix, not the prefix")
}

// TestTruncateForLLM_SmallCapStillTruncates exercises the path where
// the requested cap is smaller than preview+suffix; we cut at maxChars
// and stamp the [TRUNCATED] marker.
func TestTruncateForLLM_SmallCapStillTruncates(t *testing.T) {
	body := strings.Repeat("a", 1000)
	got := TruncateForLLM(body, 100)
	assert.True(t, len(got) > 100, "marker is appended")
	assert.True(t, strings.Contains(got, "TRUNCATED"))
}

// TestTruncateForLLM_ZeroCapUsesDefault keeps the safety net for
// callers that pass 0 (e.g. plugin returns 0 from MaxResultChars).
func TestTruncateForLLM_ZeroCapUsesDefault(t *testing.T) {
	short := strings.Repeat("a", 100)
	assert.Equal(t, short, TruncateForLLM(short, 0))
}

// TestEffectiveMaxResultChars_PluginOverride pins the precedence.
func TestEffectiveMaxResultChars_PluginOverride(t *testing.T) {
	p := truncatedPlugin{cap: 12_345}
	assert.Equal(t, 12_345, EffectiveMaxResultChars(p))
}

// TestEffectiveMaxResultChars_ZeroFallsBackToDefault confirms the
// "return 0 means use default" contract.
func TestEffectiveMaxResultChars_ZeroFallsBackToDefault(t *testing.T) {
	p := truncatedPlugin{cap: 0}
	assert.Equal(t, DefaultMaxResultChars, EffectiveMaxResultChars(p))
}

// TestEffectiveMaxResultChars_LegacyPluginGetsDefault pins the
// additive behavior: plugins without TruncationAware get the same
// 30 000 cap that's been in agent_mode forever.
func TestEffectiveMaxResultChars_LegacyPluginGetsDefault(t *testing.T) {
	assert.Equal(t, DefaultMaxResultChars, EffectiveMaxResultChars(minimalPlugin{}))
}

// TestEffectiveMaxResultChars_NilSafe documents the guard.
func TestEffectiveMaxResultChars_NilSafe(t *testing.T) {
	assert.Equal(t, DefaultMaxResultChars, EffectiveMaxResultChars(nil))
}

// TestBuiltinReadPlugin_HasGenerousTruncation pins the production
// per-tool caps. If a future PR lowers these by accident, the test
// flags the regression at the contract boundary.
func TestBuiltinReadPlugin_HasGenerousTruncation(t *testing.T) {
	assert.GreaterOrEqual(t, NewBuiltinReadPlugin().MaxResultChars(), 50_000)
	assert.GreaterOrEqual(t, NewBuiltinSearchPlugin().MaxResultChars(), 50_000)
	assert.GreaterOrEqual(t, NewBuiltinTreePlugin().MaxResultChars(), 30_000)
}
