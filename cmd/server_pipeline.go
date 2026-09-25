/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/cli/rpcserve"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/server"
	"go.uber.org/zap"
)

// serverPipelineEnv opts the gRPC server into hosting the full ChatCLI turn
// engine (ChatTurn, RunCoder, RunAgent, tools). Off by default: it starts
// the engine's workers inside the server process and serializes its turns.
const serverPipelineEnv = "CHATCLI_SERVER_PIPELINE"

// serverPipelineEnabled reads CHATCLI_SERVER_PIPELINE.
func serverPipelineEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(serverPipelineEnv)), "true")
}

// pipelineSink is the slice of server.Server initPipelineBackend needs.
type pipelineSink interface {
	SetPipelineBackend(b server.PipelineBackend)
}

// initPipelineBackend hosts a ChatCLI in the server process when
// CHATCLI_SERVER_PIPELINE=true and installs it as the pipeline backend. It
// returns the stop function to defer, or nil when the pipeline is off or
// could not start. The co-located gateway builds its own ChatCLI whose turn
// serialization the pipeline cannot join; two engines in one process would
// also run the scheduler twice, so the two features are exclusive.
func initPipelineBackend(mgr manager.LLMManager, srv pipelineSink, logger *zap.Logger) func() {
	if !serverPipelineEnabled() {
		return nil
	}
	if strings.EqualFold(os.Getenv("CHATCLI_GATEWAY_IN_SERVER"), "true") {
		logger.Error(i18n.T("cmd.server.pipeline_gateway_conflict"))
		fmt.Println(i18n.T("cmd.server.pipeline_gateway_conflict"))
		return nil
	}
	backend, stop := newRPCBackend("server", mgr, logger)
	if backend.cli == nil {
		logger.Error(i18n.T("cmd.server.pipeline_init_failed"))
		stop()
		return nil
	}
	srv.SetPipelineBackend(&pipelineAdapter{b: backend})
	logger.Info(i18n.T("cmd.server.pipeline_enabled"))
	fmt.Println(i18n.T("cmd.server.pipeline_enabled"))
	return stop
}

// pipelineAdapter presents the MCP/ACP backend as server.PipelineBackend.
type pipelineAdapter struct {
	b *rpcBackend
}

func (a *pipelineAdapter) runOpts(o server.PipelineRunOpts, emit func(string)) rpcserve.RunOpts {
	return rpcserve.RunOpts{Provider: o.Provider, Model: o.Model, Quality: o.Quality, Plain: o.Plain, Emit: emit}
}

// ChatTurn implements server.PipelineBackend.
func (a *pipelineAdapter) ChatTurn(ctx context.Context, session, text string, opts server.PipelineRunOpts) (string, error) {
	return a.b.PromptWith(ctx, session, text, a.runOpts(opts, nil))
}

// RunCoder implements server.PipelineBackend.
func (a *pipelineAdapter) RunCoder(ctx context.Context, session, task string, opts server.PipelineRunOpts, emit func(string)) (string, error) {
	return a.b.CoderStream(ctx, session, task, a.runOpts(opts, emit))
}

// RunAgent implements server.PipelineBackend.
func (a *pipelineAdapter) RunAgent(ctx context.Context, session, task string, opts server.PipelineRunOpts, emit func(string)) (string, error) {
	return a.b.AgentStream(ctx, session, task, a.runOpts(opts, emit))
}

// Tools implements server.PipelineBackend.
func (a *pipelineAdapter) Tools() []server.PipelineTool {
	tools := a.b.Tools()
	out := make([]server.PipelineTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, server.PipelineTool{Name: t.Name, Description: t.Description, Usage: t.Usage, Schema: t.Schema, ReadOnly: t.ReadOnly})
	}
	return out
}

// CallTool implements server.PipelineBackend.
func (a *pipelineAdapter) CallTool(ctx context.Context, name, args string) (string, error) {
	return a.b.CallTool(ctx, name, args)
}

// newRPCBackend builds the ChatCLI-backed RPC backend the MCP, ACP and
// gRPC pipeline surfaces share, and returns the cleanup to defer (spend
// settlement, hub resume teardown). backend.cli is nil when ChatCLI failed
// to initialize; chat then degrades to the bare passthrough.
func newRPCBackend(kind string, mgr manager.LLMManager, logger *zap.Logger) (*rpcBackend, func()) {
	provider := firstNonEmpty(os.Getenv("LLM_PROVIDER"), config.Global.GetString("LLM_PROVIDER"))
	model := firstNonEmpty(os.Getenv("LLM_MODEL"), config.Global.GetString("LLM_MODEL"))

	chatCLI, err := cli.NewChatCLI(context.Background(), mgr, logger)
	if err != nil {
		logger.Warn("rpcserve: ChatCLI init failed; agent/coder/tools disabled", zap.Error(err))
	}
	var cleanups []func()
	if chatCLI != nil {
		chatCLI.SetAuditSurface(kind)
		chatCLI.SetUnattended(true)
		chatCLI.SetRPCDangerPolicy(strings.EqualFold(os.Getenv("CHATCLI_MCP_DANGER"), "block"))
		if !strings.EqualFold(os.Getenv("CHATCLI_MCP_HUB"), "off") {
			if closeHub := chatCLI.StartHubResume(context.Background(), os.Getenv("CHATCLI_MCP_HUB_PRINCIPAL")); closeHub != nil {
				cleanups = append(cleanups, closeHub)
			}
		}
	}
	backend := &rpcBackend{
		mgr:      mgr,
		cli:      chatCLI,
		provider: provider,
		model:    model,
		sessions: map[string][]models.Message{},
	}
	if chatCLI != nil {
		backend.store = chatCLI
		chatCLI.OnManagerRebuild(backend.setManager)
		go chatCLI.CleanExpiredMachineSessionsRPC()
		cleanups = append(cleanups, func() { chatCLI.FinalizeSpend(context.Background()) })
	}
	return backend, func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
}
