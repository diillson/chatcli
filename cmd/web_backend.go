/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"context"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/cli/rpcserve"
	"github.com/diillson/chatcli/cli/webui"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/version"
)

// The methods below complete webui.Backend on the shared RPC backend, so
// the browser drives the same engine, sessions and continuity MCP and ACP
// do. Everything else webui.Backend needs already exists for those surfaces.

var _ webui.Backend = (*rpcBackend)(nil)

// Defaults are the provider and model a turn uses when it names none.
func (b *rpcBackend) Defaults() (string, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.provider, b.model
}

// SetDefaults changes the route for the turns that follow. Empty values
// keep the current ones.
func (b *rpcBackend) SetDefaults(provider, model string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if provider != "" {
		b.provider = provider
	}
	if model != "" {
		b.model = model
	}
}

func attachmentsOf(o webui.TurnOptions) *cli.TurnAttachments {
	if len(o.Images) == 0 {
		return nil
	}
	att := &cli.TurnAttachments{}
	for _, img := range o.Images {
		att.Images = append(att.Images, models.ImageContent{MediaType: img.MediaType, Data: img.Data, FileName: img.Name})
	}
	return att
}

func (b *rpcBackend) routeOf(o webui.TurnOptions) (string, string) {
	provider, model := o.Provider, o.Model
	dp, dm := b.Defaults()
	if provider == "" {
		provider = dp
	}
	if model == "" && provider == dp {
		model = dm
	}
	return provider, model
}

// Chat runs a chat turn through the full pipeline with the reply streamed
// to the sink; history and continuity are handled like PromptWith.
func (b *rpcBackend) Chat(ctx context.Context, session, text string, o webui.TurnOptions, sink cli.ChunkSink) (string, error) {
	if !b.HasLLM() {
		return "", errNoLLM
	}
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	b.refreshBound(session)
	b.mu.Lock()
	hist := append([]models.Message(nil), b.sessions[session]...)
	b.mu.Unlock()

	provider, model := b.routeOf(o)
	turn, err := b.cli.RunChatTurnRPC(ctx, session, text, hist, cli.RPCChatOpts{Provider: provider, Model: model, Attachments: attachmentsOf(o), Stream: sink})
	if err != nil {
		return "", err
	}
	newHist := capHistory(turn.History, historyCap(true))
	b.mu.Lock()
	b.sessions[session] = newHist
	b.mu.Unlock()
	b.autosaveSession(session, newHist)
	b.writeThrough(session, newHist)
	return turn.Reply, nil
}

// RunAgent drives the agent loop with structured events on the sink.
func (b *rpcBackend) RunAgent(ctx context.Context, session, task string, o webui.TurnOptions, events agentevents.Sink) (string, error) {
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	provider, model := b.routeOf(o)
	att := attachmentsOf(o)
	return b.runLoopSession(session, func(ro cli.RPCRunOpts) (string, error) {
		ro.Attachments = att
		return b.cli.RunAgentRPC(ctx, task, ro)
	}, rpcserve.RunOpts{Provider: provider, Model: model, Events: events})
}

// RunCoder drives the coder loop with structured events on the sink.
func (b *rpcBackend) RunCoder(ctx context.Context, session, task string, o webui.TurnOptions, events agentevents.Sink) (string, error) {
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	provider, model := b.routeOf(o)
	att := attachmentsOf(o)
	return b.runLoopSession(session, func(ro cli.RPCRunOpts) (string, error) {
		ro.Attachments = att
		return b.cli.RunCoderRPC(ctx, task, ro)
	}, rpcserve.RunOpts{Provider: provider, Model: model, Events: events})
}

// Commands lists the slash commands the browser may run.
func (b *rpcBackend) Commands() []rpcserve.CommandInfo { return b.ACPCommands() }

// SessionCatalog lists the saved sessions, newest first.
func (b *rpcBackend) SessionCatalog() []cli.SessionSummaryRPC {
	if b.cli == nil {
		return nil
	}
	return b.cli.SessionCatalogRPC()
}

// SessionMessages pages through one saved session.
func (b *rpcBackend) SessionMessages(name string, offset, limit int) ([]models.Message, int, error) {
	if b.cli == nil {
		return nil, 0, errCLIUnavailable
	}
	return b.cli.SessionMessagesRPC(name, offset, limit)
}

// Status is the header and settings data of the page.
func (b *rpcBackend) Status() webui.Status {
	provider, model := b.Defaults()
	st := webui.Status{Version: version.GetCurrentVersion().Version, Provider: provider, Model: model}
	if b.cli == nil {
		return st
	}
	st.PolicyMode = b.cli.PolicyModeLabel()
	st.MaxTokens = b.cli.MaxTokensRPC()
	if snap, ok := b.cli.CostSnapshotRPC(); ok {
		st.Cost = &snap
		st.DailySpentUSD, st.DailyLimitUSD = b.cli.DailyBudgetRPC()
		st.BudgetBlocked = b.cli.BudgetBlockedRPC()
	}
	st.MCP = b.cli.MCPStatusRPC()
	return st
}
