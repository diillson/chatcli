/*
 * ChatCLI - /config chat mutator tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSetChatAsk_TogglesEnv(t *testing.T) {
	prev, had := os.LookupEnv(chatAskEnvVar)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(chatAskEnvVar, prev)
		} else {
			_ = os.Unsetenv(chatAskEnvVar)
		}
	})

	c := &ChatCLI{}
	c.setChatAsk(true)
	assert.True(t, chatAskEnabled(), "after setChatAsk(true) the feature must read enabled")
	c.setChatAsk(false)
	assert.False(t, chatAskEnabled(), "after setChatAsk(false) the feature must read disabled")
}

func TestConfigChatAsk_ParsesValues(t *testing.T) {
	prev, had := os.LookupEnv(chatAskEnvVar)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(chatAskEnvVar, prev)
		} else {
			_ = os.Unsetenv(chatAskEnvVar)
		}
	})

	c := &ChatCLI{}
	c.configChatAsk([]string{"on"})
	assert.True(t, chatAskEnabled())
	c.configChatAsk([]string{"toggle"})
	assert.False(t, chatAskEnabled())
	c.configChatAsk([]string{"yes"})
	assert.True(t, chatAskEnabled())
	c.configChatAsk([]string{"off"})
	assert.False(t, chatAskEnabled())
}

func TestGetConfigChatSuggestions_Subcommands(t *testing.T) {
	c := &ChatCLI{}
	texts := suggestionTexts(c.getConfigChatSuggestions(docAt("/config chat ")))
	for _, want := range []string{"ask", "on", "off", "toggle", "status"} {
		assert.Contains(t, texts, want)
	}
}

func TestGetConfigChatSuggestions_AskValues(t *testing.T) {
	c := &ChatCLI{}
	texts := suggestionTexts(c.getConfigChatSuggestions(docAt("/config chat ask ")))
	for _, want := range []string{"on", "off", "toggle", "status"} {
		assert.Contains(t, texts, want)
	}
}
