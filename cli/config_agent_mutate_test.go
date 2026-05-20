/*
 * ChatCLI - /config agent ui mutator tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"os"
	"testing"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseUIStyle locks the token → enum mapping the mutator uses
// when the user types `/config agent ui <value>`. Case-insensitive
// + trimmed; unknown tokens return ok=false (the caller surfaces a
// friendly error instead of silently picking Full).
func TestParseUIStyle(t *testing.T) {
	cases := []struct {
		in     string
		want   agent.UIStyle
		wantOK bool
	}{
		{"full", agent.UIStyleFull, true},
		{"FULL", agent.UIStyleFull, true},
		{"  full  ", agent.UIStyleFull, true},
		{"compact", agent.UIStyleCompact, true},
		{"COMPACT", agent.UIStyleCompact, true},
		{"minimal", agent.UIStyleMinimal, true},
		{"min", agent.UIStyleMinimal, true},
		// Unknown tokens: the second return value must signal failure.
		{"banana", agent.UIStyleFull, false},
		{"", agent.UIStyleFull, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseUIStyle(tc.in)
			assert.Equal(t, tc.wantOK, ok, "ok mismatch for %q", tc.in)
			if tc.wantOK {
				assert.Equal(t, tc.want, got, "value mismatch for %q", tc.in)
			}
		})
	}
}

// TestUIStyleEnvValue is the inverse of parseUIStyle. Together they
// must round-trip: enum → string → enum returns the same enum, so
// the mutator can write the env var and the renderer can read it
// back without losing information.
func TestUIStyleEnvValue(t *testing.T) {
	for _, style := range []agent.UIStyle{
		agent.UIStyleFull, agent.UIStyleCompact, agent.UIStyleMinimal,
	} {
		t.Run(style.String(), func(t *testing.T) {
			env := uiStyleEnvValue(style)
			parsed, ok := parseUIStyle(env)
			require.True(t, ok, "env value %q must parse back", env)
			assert.Equal(t, style, parsed, "round-trip failed for %v", style)
		})
	}
}

// TestConfigAgentUI_RuntimeSwitch is the integration check: after the
// mutator runs, agent.DefaultUIStyleFromEnv must reflect the new
// value WITHOUT a process restart. This is the entire reason the
// feature exists — if the env doesn't flip, the next NewUIRenderer
// will keep producing the old style.
func TestConfigAgentUI_RuntimeSwitch(t *testing.T) {
	// Save + restore the env so the test is hermetic.
	prev, hadPrev := os.LookupEnv(agentUIStyleEnvVar)
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv(agentUIStyleEnvVar, prev)
		} else {
			_ = os.Unsetenv(agentUIStyleEnvVar)
		}
	})
	_ = os.Unsetenv(agentUIStyleEnvVar)
	require.Equal(t, agent.UIStyleFull, agent.DefaultUIStyleFromEnv(),
		"default must be Full when env unset")

	cli := &ChatCLI{}
	cli.configAgentUI([]string{"compact"})
	assert.Equal(t, agent.UIStyleCompact, agent.DefaultUIStyleFromEnv(),
		"after `ui compact`, env-resolved style must be Compact")

	cli.configAgentUI([]string{"minimal"})
	assert.Equal(t, agent.UIStyleMinimal, agent.DefaultUIStyleFromEnv(),
		"after `ui minimal`, env-resolved style must be Minimal")

	cli.configAgentUI([]string{"full"})
	assert.Equal(t, agent.UIStyleFull, agent.DefaultUIStyleFromEnv(),
		"after `ui full`, env-resolved style must be Full")
}

// TestConfigAgentUI_RejectsInvalid proves the mutator does NOT flip
// the env when the user passes garbage. Silent fallback to Full
// would erase a previously-set Compact preference on a typo.
func TestConfigAgentUI_RejectsInvalid(t *testing.T) {
	prev, hadPrev := os.LookupEnv(agentUIStyleEnvVar)
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv(agentUIStyleEnvVar, prev)
		} else {
			_ = os.Unsetenv(agentUIStyleEnvVar)
		}
	})
	require.NoError(t, os.Setenv(agentUIStyleEnvVar, "compact"))

	cli := &ChatCLI{}
	cli.configAgentUI([]string{"banana"})

	// Env must NOT have been overwritten on the bad input.
	assert.Equal(t, "compact", os.Getenv(agentUIStyleEnvVar),
		"invalid input must not clobber the existing env value")
}
