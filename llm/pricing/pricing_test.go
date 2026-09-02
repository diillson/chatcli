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

func TestRegisterLookup_CaseInsensitiveAndReplacing(t *testing.T) {
	t.Cleanup(func() { ResetProvider("devin") })

	Register("devin", "Claude-Opus-5", Rate{InputPerMTok: 5, OutputPerMTok: 25})
	r, ok := Lookup("DEVIN", "claude-opus-5")
	require.True(t, ok)
	assert.Equal(t, Rate{InputPerMTok: 5, OutputPerMTok: 25}, r)

	Register("DEVIN", "claude-opus-5", Rate{InputPerMTok: 10, OutputPerMTok: 50})
	r, _ = Lookup("devin", "CLAUDE-OPUS-5")
	assert.Equal(t, 10.0, r.InputPerMTok)

	_, ok = Lookup("devin", "unknown")
	assert.False(t, ok)
	_, ok = Lookup("openai", "claude-opus-5")
	assert.False(t, ok, "rates are provider-scoped")
}

func TestRegister_IgnoresBlankAndUnlisted(t *testing.T) {
	t.Cleanup(func() { ResetProvider("devin") })
	Register("devin", "", Rate{InputPerMTok: 1})
	Register("devin", "adaptive", Rate{})
	_, ok := Lookup("devin", "")
	assert.False(t, ok)
	_, ok = Lookup("devin", "adaptive")
	assert.False(t, ok)
}

func TestResetProvider_IsScoped(t *testing.T) {
	t.Cleanup(func() { ResetProvider("devin"); ResetProvider("other") })
	Register("devin", "a", Rate{InputPerMTok: 1})
	Register("other", "a", Rate{InputPerMTok: 1})
	ResetProvider("devin")
	_, ok := Lookup("devin", "a")
	assert.False(t, ok)
	_, ok = Lookup("other", "a")
	assert.True(t, ok)
}
