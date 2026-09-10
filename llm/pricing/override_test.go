/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pricing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseOverrides_EntriesAreIndependent(t *testing.T) {
	rates, malformed := ParseOverrides(" devin:claude-sonnet-4.6 = $3 / 15 ;DEVIN:*=1/5\nopenai:gpt-x=oops/1;broken;OLLAMA:llama=0/0")
	require.Len(t, malformed, 2, "a bad entry is reported, never taking the good ones down")
	assert.Equal(t, []string{"openai:gpt-x=oops/1", "broken"}, malformed)

	assert.Equal(t, Rate{InputPerMTok: 3, OutputPerMTok: 15}, rates[key("DEVIN", "claude-sonnet-4.6")])
	assert.Equal(t, Rate{InputPerMTok: 1, OutputPerMTok: 5}, rates[key("devin", Wildcard)])
	// An explicit 0/0 is a deliberate "free to me" and stays an entry.
	r, ok := rates[key("OLLAMA", "llama")]
	assert.True(t, ok)
	assert.Equal(t, Rate{}, r)
	assert.Len(t, rates, 3)
}

func TestParseOverrides_RejectsNegativeAndHalfEntries(t *testing.T) {
	_, malformed := ParseOverrides("DEVIN:x=-1/2;DEVIN:y=3;DEVIN=3/4;:x=1/2;DEVIN:=1/2")
	assert.Len(t, malformed, 5)
}

func TestLookupOverride_ExactBeatsWildcard_AndFollowsTheEnv(t *testing.T) {
	t.Setenv(OverrideEnv, "DEVIN:claude-opus-5=5/25;DEVIN:*=1/5")

	r, ok := LookupOverride("devin", "CLAUDE-OPUS-5")
	require.True(t, ok, "case-insensitive on both provider and model")
	assert.Equal(t, Rate{InputPerMTok: 5, OutputPerMTok: 25}, r)

	r, ok = LookupOverride("DEVIN", "swe-1.7")
	require.True(t, ok, "wildcard prices the rest of the provider")
	assert.Equal(t, Rate{InputPerMTok: 1, OutputPerMTok: 5}, r)

	_, ok = LookupOverride("OPENAI", "gpt-6-astra")
	assert.False(t, ok, "another provider is untouched")

	active, malformed := OverrideStatus()
	assert.Equal(t, 2, active)
	assert.Empty(t, malformed)

	// A changed value (what /reload produces) is picked up on the next
	// lookup with no registration step.
	t.Setenv(OverrideEnv, "DEVIN:*=2/8")
	r, ok = LookupOverride("DEVIN", "claude-opus-5")
	require.True(t, ok)
	assert.Equal(t, Rate{InputPerMTok: 2, OutputPerMTok: 8}, r)

	t.Setenv(OverrideEnv, "")
	_, ok = LookupOverride("DEVIN", "claude-opus-5")
	assert.False(t, ok, "an empty value clears every override")
}
