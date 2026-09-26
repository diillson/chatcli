/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
)

// rpcChatFakeManager hands out one client whatever the route asked for, so a
// provider-only call can be resolved without a real provider.
type rpcChatFakeManager struct {
	minimalManager
	c client.LLMClient
}

func (m *rpcChatFakeManager) GetClient(string, string) (client.LLMClient, error) { return m.c, nil }

// A headless chat turn (MCP, ACP, gateway, web UI) is a turn of this process:
// the dashboard gets the same turn node the REPL turn has, and the session
// node learns the route the turn resolved.
func TestRunChatTurnRPC_ReportsTurnAndRouteOnTheDash(t *testing.T) {
	turns := pulseWatch(t, pulse.KindTurn)
	sessions := pulseWatch(t, pulse.KindSession)
	fake := &rpcChatFakeClient{reply: "ok"}
	c := newRPCChatCLI(t, fake)

	_, err := c.RunChatTurnRPC(context.Background(), "sess-pulse", "hi there", nil, RPCChatOpts{})
	require.NoError(t, err)

	evs := turns(2)
	assert.Equal(t, pulse.PhaseStart, evs[0].Phase)
	assert.Equal(t, pulseTurnChat, evs[0].Name)
	assert.Equal(t, pulse.PhaseEnd, evs[1].Phase)
	assert.Equal(t, pulse.StatusOK, evs[1].Status)

	route := sessions(1)[0]
	assert.Equal(t, pulse.PhaseUpdate, route.Phase)
	assert.Equal(t, "FAKE", route.Attrs["provider"])
	assert.Equal(t, "fake-model", route.Attrs["model"])
	assert.Equal(t, pulseRouteSession, route.Attrs["route"], "the resolver kept the session's client")
	assert.Equal(t, "rpc chat turn", route.Attrs["via"])
}

// A failed turn closes its node as an error, never as ok.
func TestRunChatTurnRPC_TurnNodeCarriesTheFailure(t *testing.T) {
	turns := pulseWatch(t, pulse.KindTurn)
	fake := &rpcChatFakeClient{err: assert.AnError}
	c := newRPCChatCLI(t, fake)

	_, err := c.RunChatTurnRPC(context.Background(), "sess-fail", "hi", nil, RPCChatOpts{})
	require.Error(t, err)
	evs := turns(2)
	assert.Equal(t, pulse.StatusError, evs[1].Status)
}

// A call that names the provider only is served by that provider's default
// model, and the turn is recorded under the model that served it: an empty
// name filed the usage under "provider:" (unpriced, so the turn cost nothing
// on /cost) and opened a nameless model node on the dashboard.
func TestResolveRPCChatClient_ProviderOnlyCallNamesTheServingModel(t *testing.T) {
	fake := &rpcChatFakeClient{reply: "ok"}
	c := newRPCChatCLI(t, fake)
	c.manager = &rpcChatFakeManager{c: fake}

	_, provider, model, err := c.resolveRPCChatClient("", RPCChatOpts{Provider: "FAKE"})
	require.NoError(t, err)
	assert.Equal(t, "FAKE", provider)
	assert.Equal(t, "fake-model", model, "the session's model id on the session's provider, not the empty request")

	c.Model = ""
	_, _, model, err = c.resolveRPCChatClient("", RPCChatOpts{Provider: "FAKE"})
	require.NoError(t, err)
	assert.Equal(t, "fake-model", model, "with no session model the client's own name is the last resort")

	_, _, model, err = c.resolveRPCChatClient("", RPCChatOpts{Provider: "CLAUDEAI"})
	require.NoError(t, err)
	assert.NotEmpty(t, model, "another provider gets its default model id")
	assert.NotContains(t, model, " ", "an id, never a display name")
	c.Model = "fake-model"

	_, _, model, err = c.resolveRPCChatClient("", RPCChatOpts{Provider: "FAKE", Model: "fake-2"})
	require.NoError(t, err)
	assert.Equal(t, "fake-2", model, "an explicit model is kept as asked")

	_, provider, model, err = c.resolveRPCChatClient("", RPCChatOpts{Model: "fake-3"})
	require.NoError(t, err)
	assert.Equal(t, "FAKE", provider, "a model-only call stays on the session's provider")
	assert.Equal(t, "fake-3", model)
}

// contextWindowUsage is the one projection the footer and the dashboard
// share: the model's window (the catalog's conservative default for a model
// it does not know) and a positive share of it once tokens were spent.
func TestContextWindowUsage_ReportsWindowAndShare(t *testing.T) {
	c := newRPCChatCLI(t, &rpcChatFakeClient{})
	usage := &models.UsageInfo{PromptTokens: 20000, CompletionTokens: 100}

	pct, reserve, window := c.contextWindowUsage("CLAUDEAI", "claude-sonnet-4-6", usage)
	require.Greater(t, window, 0)
	assert.Greater(t, pct, 0.0)
	assert.GreaterOrEqual(t, reserve, 0.0)

	_, _, fallback := c.contextWindowUsage("FAKE", "fake-model", usage)
	assert.Greater(t, fallback, 0, "an unknown model still gets the catalog's default window")
	assert.Less(t, fallback, window, "the default is smaller than a frontier model's window")
}
