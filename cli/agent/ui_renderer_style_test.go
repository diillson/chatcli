/*
 * ChatCLI - UIRenderer style dispatcher tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDefaultUIStyleFromEnv locks the mapping from CHATCLI_CODER_UI to
// the UIStyle enum so any future renaming of the env values (or the
// addition of a new style) has to update this table on purpose.
// The cross-mode unification PR made this var drive BOTH /coder and
// /agent, so a silent change here would affect both surfaces.
func TestDefaultUIStyleFromEnv(t *testing.T) {
	t.Setenv("CHATCLI_CODER_UI", "")

	cases := []struct {
		envVal string
		want   UIStyle
	}{
		{"", UIStyleFull},
		{"full", UIStyleFull},
		{"false", UIStyleFull},
		{"0", UIStyleFull},
		{"compact", UIStyleCompact},
		{"minimal", UIStyleMinimal},
		{"min", UIStyleMinimal},
		{"true", UIStyleMinimal},
		{"1", UIStyleMinimal},
		// Unrecognized values fall back to Full rather than crashing.
		{"banana", UIStyleFull},
		// Trim + case-fold: matches the production normalisation.
		{"  COMPACT  ", UIStyleCompact},
	}
	for _, tc := range cases {
		t.Run(tc.envVal, func(t *testing.T) {
			if tc.envVal == "" {
				_ = os.Unsetenv("CHATCLI_CODER_UI")
			} else {
				t.Setenv("CHATCLI_CODER_UI", tc.envVal)
			}
			got := DefaultUIStyleFromEnv()
			assert.Equal(t, tc.want, got,
				"env=%q expected style %v, got %v", tc.envVal, tc.want, got)
		})
	}
}

// TestUIRendererStyleAccessors proves the renderer honors the style it
// was constructed with. The IsFull/IsCompact/IsMinimal triple is the
// public contract that agent_mode.go relies on for cross-mode style
// routing — if any of them regress, the wrong renderer path fires.
func TestUIRendererStyleAccessors(t *testing.T) {
	cases := []struct {
		style       UIStyle
		wantFull    bool
		wantCompact bool
		wantMinimal bool
		wantName    string
	}{
		{UIStyleFull, true, false, false, "full"},
		{UIStyleCompact, false, true, false, "compact"},
		{UIStyleMinimal, false, false, true, "minimal"},
	}
	for _, tc := range cases {
		t.Run(tc.wantName, func(t *testing.T) {
			r := NewUIRendererWithStyle(nil, tc.style)
			assert.Equal(t, tc.wantFull, r.IsFull(), "IsFull")
			assert.Equal(t, tc.wantCompact, r.IsCompact(), "IsCompact")
			assert.Equal(t, tc.wantMinimal, r.IsMinimal(), "IsMinimal")
			assert.Equal(t, tc.wantName, r.Style().String(), "Style.String")
		})
	}
}

// TestNewUIRendererReadsEnv proves the no-arg constructor wires the env
// var through DefaultUIStyleFromEnv. Without this regression test, a
// future change that swaps NewUIRenderer to default-to-Full would mask
// user intent silently — the user sets the env, gets Full anyway.
func TestNewUIRendererReadsEnv(t *testing.T) {
	t.Setenv("CHATCLI_CODER_UI", "compact")
	r := NewUIRenderer(nil)
	assert.True(t, r.IsCompact(), "expected NewUIRenderer to inherit CHATCLI_CODER_UI=compact")

	t.Setenv("CHATCLI_CODER_UI", "minimal")
	r = NewUIRenderer(nil)
	assert.True(t, r.IsMinimal(), "expected NewUIRenderer to inherit CHATCLI_CODER_UI=minimal")

	_ = os.Unsetenv("CHATCLI_CODER_UI")
	r = NewUIRenderer(nil)
	assert.True(t, r.IsFull(), "expected NewUIRenderer to default to Full when env unset")
}
