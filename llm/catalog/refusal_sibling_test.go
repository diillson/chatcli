/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRefusalSibling_WalksTheFamilyOnTheSameProvider(t *testing.T) {
	assert.Equal(t, "CLAUDEAI:claude-opus-5", RefusalSibling("CLAUDEAI", "claude-fable-5-1"))
	assert.Equal(t, "CLAUDEAI:claude-sonnet-5", RefusalSibling("claudeai", "claude-opus-5"))
	assert.Equal(t, "CLAUDEAI:claude-haiku-4-5-20251001", RefusalSibling("CLAUDEAI", "claude-sonnet-5"))
	assert.Empty(t, RefusalSibling("CLAUDEAI", "claude-haiku-4-5-20251001"), "no sibling below Haiku")
	assert.Empty(t, RefusalSibling("OPENAI", "gpt-5.6"), "auto knows the Anthropic families only")
	assert.Empty(t, RefusalSibling("GOOGLEAI", "claude-fable-5-1"), "a provider without the sibling gets none")
}

func TestSplitRouteHandle(t *testing.T) {
	p, m, ok := SplitRouteHandle("claudeai:claude-sonnet-5")
	assert.True(t, ok)
	assert.Equal(t, "CLAUDEAI", p)
	assert.Equal(t, "claude-sonnet-5", m)
	for _, bad := range []string{"", "claude-sonnet-5", ":x", "CLAUDEAI:"} {
		_, _, ok := SplitRouteHandle(bad)
		assert.False(t, ok, bad)
	}
}
