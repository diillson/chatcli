/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Package cmd — rpcserve.go
 *
 * Implements the `chatcli mcp-server` and `chatcli acp` subcommands, which run
 * ChatCLI as a JSON-RPC server over stdio:
 *
 *   mcp-server : exposes ChatCLI as an MCP server. Beyond a chat tool, it
 *                exposes the agent and coder loops and the curated built-in
 *                tools, so an MCP client can drive ChatCLI's real
 *                functionality — not just Q&A.
 *   acp        : exposes ChatCLI over the Agent Client Protocol (editors).
 *
 * stdin/stdout carry the protocol; all logging goes to the file logger. The
 * agent/coder render to stdout, so the backend captures that during a run —
 * the JSON-RPC server kept its own copy of the original stdout, so the
 * protocol channel is never corrupted.
 */
package cmd

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/cli/rpcserve"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/version"
	"go.uber.org/zap"
)

// RunMCPServe runs the MCP server over stdio.
func RunMCPServe(args []string, mgr manager.LLMManager, logger *zap.Logger) error {
	return runRPC("mcp", mgr, logger)
}

// RunACP runs the ACP server over stdio.
func RunACP(args []string, mgr manager.LLMManager, logger *zap.Logger) error {
	return runRPC("acp", mgr, logger)
}

func runRPC(kind string, mgr manager.LLMManager, logger *zap.Logger) error {
	provider := firstNonEmpty(os.Getenv("LLM_PROVIDER"), config.Global.GetString("LLM_PROVIDER"))
	model := firstNonEmpty(os.Getenv("LLM_MODEL"), config.Global.GetString("LLM_MODEL"))

	// A full ChatCLI gives the backend access to the agent/coder loops and the
	// built-in tools. Failure is non-fatal: chat still works via the manager.
	chatCLI, err := cli.NewChatCLI(context.Background(), mgr, logger)
	if err != nil {
		logger.Warn("rpcserve: ChatCLI init failed; agent/coder/tools disabled", zap.Error(err))
	}

	backend := &rpcBackend{
		mgr:      mgr,
		cli:      chatCLI,
		provider: provider,
		model:    model,
		sessions: map[string][]models.Message{},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ver := version.GetCurrentVersion().Version
	switch kind {
	case "acp":
		a := rpcserve.NewACP(backend, ver)
		srv := rpcserve.NewServer(os.Stdin, os.Stdout, a.Handle)
		a.SetNotifier(srv.Notify)
		logger.Info("acp: serving over stdio")
		return srv.Serve(ctx)
	default: // mcp
		m := rpcserve.NewMCP(backend, "chatcli", ver)
		srv := rpcserve.NewServer(os.Stdin, os.Stdout, m.Handle)
		logger.Info("mcp-server: serving over stdio")
		return srv.Serve(ctx)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// rpcBackend implements rpcserve.MCPBackend (and thus Backend). Chat keeps a
// per-session history; agent/coder/tools delegate to the ChatCLI.
type rpcBackend struct {
	mgr      manager.LLMManager
	cli      *cli.ChatCLI
	provider string
	model    string

	mu       sync.Mutex
	sessions map[string][]models.Message
}

const rpcMaxHistory = 30

// Prompt implements the chat capability with per-session history.
func (b *rpcBackend) Prompt(ctx context.Context, session, text string) (string, error) {
	client, err := b.mgr.GetClient(b.provider, b.model)
	if err != nil {
		return "", err
	}

	b.mu.Lock()
	hist := append([]models.Message(nil), b.sessions[session]...)
	b.mu.Unlock()

	hist = append(hist, models.Message{Role: "user", Content: text})
	reply, err := client.SendPrompt(ctx, text, hist, 0)
	if err != nil {
		return "", err
	}
	hist = append(hist, models.Message{Role: "assistant", Content: reply})

	b.mu.Lock()
	if len(hist) > rpcMaxHistory {
		hist = hist[len(hist)-rpcMaxHistory:]
	}
	b.sessions[session] = hist
	b.mu.Unlock()

	return reply, nil
}

// Agent runs the full agent loop and returns its transcript.
func (b *rpcBackend) Agent(ctx context.Context, _, task string) (string, error) {
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	return b.cli.RunAgentCaptured(ctx, task)
}

// Coder runs the coder loop and returns its transcript.
func (b *rpcBackend) Coder(ctx context.Context, _, task string) (string, error) {
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	return b.cli.RunCoderCaptured(ctx, task)
}

// BuiltinTools lists the curated built-in tools exposed over MCP.
func (b *rpcBackend) BuiltinTools() []rpcserve.ToolInfo {
	if b.cli == nil {
		return nil
	}
	tools := b.cli.ListBuiltinTools()
	out := make([]rpcserve.ToolInfo, 0, len(tools))
	for _, t := range tools {
		out = append(out, rpcserve.ToolInfo{Name: t.Name, Description: t.Description})
	}
	return out
}

// CallBuiltin invokes a curated built-in tool by name.
func (b *rpcBackend) CallBuiltin(ctx context.Context, name, args string) (string, error) {
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	return b.cli.RunBuiltinTool(ctx, name, args)
}

type errCLI string

func (e errCLI) Error() string { return string(e) }

var errCLIUnavailable = errCLI("agent/coder/tools unavailable: ChatCLI failed to initialize")
