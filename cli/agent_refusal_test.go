/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
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

// With the fallback off, a refused turn is resent with a nudge the model
// reads, on the outgoing turn and in the run's history; a run keeps at
// most two such resends.
func TestResendTurnAfterRefusalNudgesAndIsBounded(t *testing.T) {
	t.Setenv(refusalFallbackEnv, "off")
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
	assert.Empty(t, cliObj.agentRouteOverrideHandle(), "off: the resend stays on the same model")

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
	assert.Empty(t, cliObj.agentRouteOverrideHandle())
}

// The default: the resend goes to a sibling model of the same provider for
// that one turn, and the next turn is back on the user's model. In the
// field the same conversation was refused again on the same model.
func TestRefusalFallbackServesOneTurnThenHandsBack(t *testing.T) {
	t.Setenv(refusalFallbackEnv, "auto")
	cliObj, _ := newRoutingTestCLI()
	cliObj.unattended = true
	cliObj.Model = "claude-fable-5-1"
	cliObj.Client = &routingStubClient{model: "claude-fable-5-1"}
	a := &AgentMode{cli: cliObj, logger: zap.NewNop()}
	ctx := context.Background()
	turn := []models.Message{{Role: "user", Content: "x"}}

	require.True(t, a.resendTurnAfter(refusalErr(), &turn))
	assert.Equal(t, "CLAUDEAI:claude-opus-5", cliObj.agentRouteOverrideHandle(), "Fable falls back to Opus on the same provider")
	assert.Len(t, turn, 2, "the nudge travels with the resend")

	// The resend resolves its client: it is the fallback turn.
	turnClient, _ := a.clientAndCtxForTurn(ctx)
	assert.Equal(t, "claude-opus-5", turnClient.GetModelName())
	assert.Equal(t, "CLAUDEAI:claude-opus-5", cliObj.agentRouteOverrideHandle(), "still routed during the fallback turn")

	// The next turn hands the route back.
	turnClient, _ = a.clientAndCtxForTurn(ctx)
	assert.Equal(t, "claude-fable-5-1", turnClient.GetModelName())
	assert.Empty(t, cliObj.agentRouteOverrideHandle())
	assert.Empty(t, a.refusalFallback)
}

// A user's own @model override is what the route returns to, not nothing.
func TestRefusalFallbackRestoresTheUsersOverride(t *testing.T) {
	t.Setenv(refusalFallbackEnv, "CLAUDEAI:claude-haiku-4-5-20251001")
	cliObj, _ := newRoutingTestCLI()
	cliObj.unattended = true
	cliObj.setAgentRouteOverride("GOOGLEAI:gemini-2.5-flash", "@model use")
	a := &AgentMode{cli: cliObj, logger: zap.NewNop()}
	ctx := context.Background()

	require.True(t, a.resendTurnAfter(refusalErr(), nil))
	assert.Equal(t, "CLAUDEAI:claude-haiku-4-5-20251001", cliObj.agentRouteOverrideHandle(), "an explicit handle is used as given")
	a.clientAndCtxForTurn(ctx) // the fallback turn
	a.clientAndCtxForTurn(ctx) // the turn after
	assert.Equal(t, "GOOGLEAI:gemini-2.5-flash", cliObj.agentRouteOverrideHandle())
}

// Past the retry budget the fallback stays for the run: paying for a
// refused request on every turn is what the budget is there to stop.
func TestRefusalFallbackBecomesStickyAndResetsPerRun(t *testing.T) {
	t.Setenv(refusalFallbackEnv, "auto")
	cliObj, _ := newRoutingTestCLI()
	cliObj.unattended = true
	cliObj.Model = "claude-fable-5-1"
	a := &AgentMode{cli: cliObj, logger: zap.NewNop()}
	ctx := context.Background()

	for i := 1; i <= agentRefusalMaxRetries; i++ {
		require.True(t, a.resendTurnAfter(refusalErr(), nil), "refusal %d", i)
		a.clientAndCtxForTurn(ctx) // fallback turn
		a.clientAndCtxForTurn(ctx) // back on Fable
		assert.Empty(t, cliObj.agentRouteOverrideHandle(), "refusal %d: one-turn fallback", i)
	}
	require.True(t, a.resendTurnAfter(refusalErr(), nil), "a third refusal does not end the run")
	assert.True(t, a.refusalSticky)
	for i := 0; i < 3; i++ {
		a.clientAndCtxForTurn(ctx)
		assert.Equal(t, "CLAUDEAI:claude-opus-5", cliObj.agentRouteOverrideHandle(), "sticky: the route stays")
	}
	require.True(t, a.resendTurnAfter(refusalErr(), nil), "refused on the fallback too: one more resend, same route")
	assert.Equal(t, "CLAUDEAI:claude-opus-5", cliObj.agentRouteOverrideHandle())

	// A new run starts clean, on the user's model. (Run clears the override
	// itself; the refusal state must not resurrect it.)
	a.resetPerRunState()
	cliObj.clearAgentRouteOverride("run start")
	assert.False(t, a.refusalSticky)
	assert.Equal(t, 0, a.refusalRetries)
	a.clientAndCtxForTurn(ctx)
	assert.Empty(t, cliObj.agentRouteOverrideHandle())
}

func TestRefusalSiblingFor(t *testing.T) {
	assert.Equal(t, "CLAUDEAI:claude-opus-5", refusalSiblingFor("CLAUDEAI", "claude-fable-5-1"))
	assert.Equal(t, "CLAUDEAI:claude-sonnet-5", refusalSiblingFor("claudeai", "claude-opus-5"))
	assert.Equal(t, "CLAUDEAI:claude-haiku-4-5-20251001", refusalSiblingFor("CLAUDEAI", "claude-sonnet-5"))
	assert.Empty(t, refusalSiblingFor("CLAUDEAI", "claude-haiku-4-5-20251001"), "no sibling below Haiku")
	assert.Empty(t, refusalSiblingFor("OPENAI", "gpt-5.6"), "auto knows the Anthropic families only")
	assert.Empty(t, refusalSiblingFor("GOOGLEAI", "claude-fable-5-1"), "a provider without the sibling gets none")
}

// The notice reaches the terminal when there is one.
func TestRefusalNoticePrintsOnATerminal(t *testing.T) {
	t.Setenv(refusalFallbackEnv, "auto")
	cliObj, _ := newRoutingTestCLI()
	cliObj.Model = "claude-fable-5-1"
	a := &AgentMode{cli: cliObj, logger: zap.NewNop()}
	turn := []models.Message{}
	out := captureStdout(t, func() { require.True(t, a.resendTurnAfter(refusalErr(), &turn)) })
	assert.Contains(t, out, "claude-opus-5")
	assert.Contains(t, out, "1/2")
}
