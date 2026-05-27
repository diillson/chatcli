/*
 * ChatCLI - /hub and /config hub completer tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestGetHubSuggestions_Subcommands(t *testing.T) {
	c := &ChatCLI{}
	texts := suggestionTexts(c.getHubSuggestions(docAt("/hub ")))
	for _, want := range []string{"whoami", "bind", "bindings"} {
		assert.Contains(t, texts, want)
	}
}

func TestGetHubSuggestions_BindPlatforms(t *testing.T) {
	c := &ChatCLI{}
	texts := suggestionTexts(c.getHubSuggestions(docAt("/hub bind ")))
	for _, want := range []string{"telegram", "slack", "whatsapp", "discord", "webhook"} {
		assert.Contains(t, texts, want)
	}
}

func TestGetHubSuggestions_BindPrincipalSlot(t *testing.T) {
	c := &ChatCLI{}
	c.hubSync = newHubSync(newFakeHubClient(), zap.NewNop())
	// /hub bind <platform> <userid> <TAB> → known principals (at least "default")
	texts := suggestionTexts(c.getHubSuggestions(docAt("/hub bind telegram 123 ")))
	assert.Contains(t, texts, defaultHubPrincipal)
}

func TestGetConfigHubSuggestions_SetReset(t *testing.T) {
	c := &ChatCLI{}
	texts := suggestionTexts(c.getConfigHubSuggestions(docAt("/config hub ")))
	assert.Contains(t, texts, "set")
	assert.Contains(t, texts, "reset")
}

func TestGetConfigHubSuggestions_Keys(t *testing.T) {
	c := &ChatCLI{}
	for _, prefix := range []string{"/config hub set ", "/config hub reset "} {
		texts := suggestionTexts(c.getConfigHubSuggestions(docAt(prefix)))
		for _, want := range []string{hubKeyEnabled, hubKeyPrincipal, hubKeyIsolate, hubKeyTTLHours} {
			assert.Contains(t, texts, want, "prefix %q must offer key %q", prefix, want)
		}
	}
}

func TestGetConfigHubSuggestions_BoolValues(t *testing.T) {
	c := &ChatCLI{}
	for _, key := range []string{hubKeyEnabled, hubKeyIsolate} {
		texts := suggestionTexts(c.getConfigHubSuggestions(docAt("/config hub set " + key + " ")))
		assert.Contains(t, texts, "on")
		assert.Contains(t, texts, "off")
	}
}

func TestGetConfigHubSuggestions_TTLPresets(t *testing.T) {
	c := &ChatCLI{}
	texts := suggestionTexts(c.getConfigHubSuggestions(docAt("/config hub set ttl_hours ")))
	for _, want := range []string{"6", "24", "72", "168", "0"} {
		assert.Contains(t, texts, want)
	}
}

func TestGetConfigHubSuggestions_PrincipalValues(t *testing.T) {
	c := &ChatCLI{}
	c.hubSync = newHubSync(newFakeHubClient(), zap.NewNop())
	texts := suggestionTexts(c.getConfigHubSuggestions(docAt("/config hub set principal ")))
	assert.Contains(t, texts, defaultHubPrincipal)
}

func TestKnownHubPrincipals_NilAndFake(t *testing.T) {
	// No session: still returns the default so completion is never empty.
	c := &ChatCLI{}
	assert.Contains(t, c.knownHubPrincipals(), defaultHubPrincipal)

	// With a fake hub session, the helper queries bindings without error.
	c.hubSync = newHubSync(newFakeHubClient(), zap.NewNop())
	assert.Contains(t, c.knownHubPrincipals(), defaultHubPrincipal)
}
