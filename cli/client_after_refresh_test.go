/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/stretchr/testify/assert"
)

func TestClientAfterRefresh_PicksTheRebuiltDefaultOnlyWhenTheTurnRanOnIt(t *testing.T) {
	routed := &client.MockLLMClient{Response: "routed"}
	rebuilt := &client.MockLLMClient{Response: "rebuilt"}
	cli := &ChatCLI{Client: rebuilt}

	assert.Same(t, rebuilt, cli.clientAfterRefresh(routed, false), "the default client expired and was rebuilt: retry on it")
	assert.Same(t, routed, cli.clientAfterRefresh(routed, true), "a skill or override routed the turn elsewhere: that client did not expire")

	cli.Client = nil
	assert.Same(t, routed, cli.clientAfterRefresh(routed, false), "no rebuilt client: keep what we had")
}
