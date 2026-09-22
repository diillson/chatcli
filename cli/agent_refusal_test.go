/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"errors"
	"fmt"
	"testing"

	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func refusalErr() error {
	return fmt.Errorf("turn: %w", &llmclient.EmptyResponseError{Provider: "ClaudeAI", Model: "claude-fable-5-1", StopReason: llmclient.StopReasonRefusal})
}

// A refused turn is resent with a nudge the model reads, on the outgoing
// turn and in the run's history; a run keeps at most two such resends.
func TestResendTurnAfterRefusalNudgesAndIsBounded(t *testing.T) {
	cliObj, _ := newRoutingTestCLI()
	cliObj.unattended = true // no terminal to print the notice to
	cliObj.history = []models.Message{{Role: "user", Content: "build the api"}}
	a := &AgentMode{cli: cliObj, logger: zap.NewNop()}
	turn := []models.Message{{Role: "user", Content: "build the api"}, {Role: "assistant", Content: "reading"}}

	require.True(t, a.resendTurnAfter(refusalErr(), &turn))
	require.Len(t, turn, 3)
	assert.Equal(t, "user", turn[2].Role)
	assert.Contains(t, turn[2].Content, "stop_reason: refusal")
	assert.Contains(t, turn[2].Content, "nothing was executed")
	require.Len(t, cliObj.history, 2, "the nudge is part of the run's history too")
	assert.Equal(t, refusalNudge, cliObj.history[1].Content)
	assert.Equal(t, 1, a.refusalRetries)

	require.True(t, a.resendTurnAfter(refusalErr(), &turn))
	assert.Equal(t, 2, a.refusalRetries)
	assert.False(t, a.resendTurnAfter(refusalErr(), &turn), "a third refusal ends the run with the cause")
	assert.Len(t, turn, 4, "no nudge is appended once the run gives up")

	a.resetPerRunState()
	assert.Equal(t, 0, a.refusalRetries, "the budget is per run")
	require.True(t, a.resendTurnAfter(refusalErr(), nil), "a nil outgoing history is tolerated")
}

func TestResendTurnAfterIgnoresOtherFailures(t *testing.T) {
	cliObj, _ := newRoutingTestCLI()
	cliObj.unattended = true
	a := &AgentMode{cli: cliObj, logger: zap.NewNop()}
	turn := []models.Message{{Role: "user", Content: "x"}}

	assert.False(t, a.resendTurnAfter(nil, &turn))
	assert.False(t, a.resendTurnAfter(errors.New("connection reset"), &turn))
	maxed := &llmclient.EmptyResponseError{Provider: "ClaudeAI", StopReason: llmclient.StopReasonMaxTokens}
	assert.False(t, a.resendTurnAfter(maxed, &turn), "an empty reply that is not a refusal takes the existing recovery paths")
	assert.Len(t, turn, 1)
	assert.Empty(t, cliObj.history)
	assert.Equal(t, 0, a.refusalRetries)
}

// The notice reaches the terminal when there is one.
func TestRefusalNoticePrintsOnATerminal(t *testing.T) {
	cliObj, _ := newRoutingTestCLI()
	a := &AgentMode{cli: cliObj, logger: zap.NewNop()}
	turn := []models.Message{}
	out := captureStdout(t, func() { require.True(t, a.resendTurnAfter(refusalErr(), &turn)) })
	assert.Contains(t, out, "1/2")
}
