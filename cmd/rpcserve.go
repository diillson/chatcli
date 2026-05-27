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
 *   mcp-server : exposes ChatCLI as an MCP server (tools for any MCP client).
 *   acp       : exposes ChatCLI over the Agent Client Protocol (editors).
 *
 * stdin/stdout carry the protocol, so all logging goes to the file logger —
 * nothing else may touch stdout or it would corrupt the stream.
 */
package cmd

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

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
	backend := &rpcBackend{mgr: mgr, provider: provider, model: model, logger: logger, sessions: map[string][]models.Message{}}

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

// rpcBackend implements rpcserve.Backend, keeping per-session history and
// routing prompts to the configured provider/model.
type rpcBackend struct {
	mgr      manager.LLMManager
	provider string
	model    string
	logger   *zap.Logger

	mu       sync.Mutex
	sessions map[string][]models.Message
}

const rpcMaxHistory = 30

// Prompt implements rpcserve.Backend.
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
