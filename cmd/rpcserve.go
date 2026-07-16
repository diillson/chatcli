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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
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
	if chatCLI != nil {
		// stdin carries the JSON-RPC protocol: any interactive confirmation
		// would consume protocol frames and hang the run. Unattended mode
		// auto-approves every prompt at the source (same regime as the
		// gateway daemon); CHATCLI_MCP_DANGER=block re-arms the dangerous-
		// command gate as an in-band refusal instead of a stdin read.
		chatCLI.SetUnattended(true)
		chatCLI.SetRPCDangerPolicy(strings.EqualFold(os.Getenv("CHATCLI_MCP_DANGER"), "block"))
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
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ver := version.GetCurrentVersion().Version
	switch kind {
	case "acp":
		a := rpcserve.NewACP(backend, ver)
		srv := rpcserve.NewServer(os.Stdin, os.Stdout, a.Handle)
		defer quarantineStdout(logger)()
		a.SetNotifier(srv.Notify)
		logger.Info("acp: serving over stdio")
		return srv.Serve(ctx)
	default: // mcp
		m := rpcserve.NewMCP(backend, "chatcli", ver)
		srv := rpcserve.NewServer(os.Stdin, os.Stdout, m.Handle)
		defer quarantineStdout(logger)()
		// Relay catalog changes from ChatCLI's own MCP client (servers
		// connecting after startup, dynamic refreshes, disconnects) to our
		// client as notifications/tools/list_changed, so proxied tools show
		// up without a reconnect.
		if chatCLI != nil {
			chatCLI.SetMCPToolsObserver(func() {
				_ = srv.Notify(rpcserve.ToolsListChangedMethod, map[string]interface{}{})
			})
		}
		logger.Info("mcp-server: serving over stdio")
		return srv.Serve(ctx)
	}
}

// quarantineStdout re-points the process-global os.Stdout at a pipe drained
// into the logger and returns the restore function. The JSON-RPC server
// captured the real stdout at construction, so the protocol keeps its channel;
// after this, any stray print outside a captured agent/coder run (memory
// notices, skill notices, plugins writing directly) lands in the log instead
// of interleaving with protocol frames. captureStreaming keeps working: it
// saves and restores the os.Stdout *variable*, which now points at the sink.
func quarantineStdout(logger *zap.Logger) func() {
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		logger.Warn("rpcserve: stdout quarantine unavailable", zap.Error(err))
		return func() {}
	}
	os.Stdout = w
	done := make(chan struct{})
	go func() {
		defer close(done)
		br := bufio.NewReader(r)
		for {
			line, rerr := br.ReadString('\n')
			if s := strings.TrimSpace(line); s != "" {
				logger.Debug("stdout quarantined", zap.String("line", s))
			}
			if rerr != nil {
				return
			}
		}
	}()
	return func() {
		os.Stdout = orig
		_ = w.Close()
		<-done
		_ = r.Close()
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

// sessionStore is the slice of ChatCLI the session actions need: the
// persistent saved-session store shared with the REPL /session command.
// An interface so ManageSession is testable without a full ChatCLI.
type sessionStore interface {
	SaveSessionRPC(name string, history []models.Message) error
	LoadSessionRPC(name string) ([]models.Message, error)
	ListSessionsRPC() ([]string, error)
	DeleteSessionRPC(name string) error
}

// rpcBackend implements rpcserve.MCPBackend (and thus Backend). Chat keeps a
// per-session history; agent/coder/tools delegate to the ChatCLI.
type rpcBackend struct {
	mgr      manager.LLMManager
	cli      *cli.ChatCLI
	store    sessionStore // nil when ChatCLI failed to initialize
	provider string
	model    string

	mu       sync.Mutex
	sessions map[string][]models.Message
}

const rpcMaxHistory = 30

// HasLLM reports whether any LLM provider is configured. The MCP server
// hides the LLM-backed harness tools when this is false.
func (b *rpcBackend) HasLLM() bool {
	return len(b.mgr.GetAvailableProviders()) > 0
}

// errNoLLM is the actionable no-provider error, sent in-band to the caller
// (a model): it explains what still works and how to enable the rest.
var errNoLLM = errCLI("no LLM provider is configured in this ChatCLI instance, so chat/agent/coder tools are unavailable. " +
	"Every direct tool (read, search, web, memory, knowledge, …) keeps working with your own model. " +
	"To enable the harness tools, configure a provider in ChatCLI (an API key env var or 'chatcli' + '/auth login <provider>') and restart the server.")

// Prompt implements the chat capability with per-session history.
func (b *rpcBackend) Prompt(ctx context.Context, session, text string) (string, error) {
	if !b.HasLLM() {
		return "", errNoLLM
	}
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

// PromptWith is chat with optional per-call provider/model routing.
func (b *rpcBackend) PromptWith(ctx context.Context, session, text string, opts rpcserve.RunOpts) (string, error) {
	if opts.Provider == "" && opts.Model == "" {
		return b.Prompt(ctx, session, text)
	}
	provider := opts.Provider
	if provider == "" {
		provider = b.provider
	}
	client, err := b.mgr.GetClient(provider, opts.Model)
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

// Agent runs the full agent loop with per-call options.
func (b *rpcBackend) Agent(ctx context.Context, _, task string, opts rpcserve.RunOpts) (string, error) {
	if !b.HasLLM() {
		return "", errNoLLM
	}
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	return b.cli.RunAgentRPC(ctx, task, toRunOpts(opts))
}

// Coder runs the coder loop with per-call options.
func (b *rpcBackend) Coder(ctx context.Context, _, task string, opts rpcserve.RunOpts) (string, error) {
	if !b.HasLLM() {
		return "", errNoLLM
	}
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	return b.cli.RunCoderRPC(ctx, task, toRunOpts(opts))
}

// AgentStream / CoderStream are the ACP streaming variants.
func (b *rpcBackend) AgentStream(ctx context.Context, _, task string, opts rpcserve.RunOpts) (string, error) {
	if !b.HasLLM() {
		return "", errNoLLM
	}
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	return b.cli.RunAgentRPC(ctx, task, toRunOpts(opts))
}

func (b *rpcBackend) CoderStream(ctx context.Context, _, task string, opts rpcserve.RunOpts) (string, error) {
	if !b.HasLLM() {
		return "", errNoLLM
	}
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	return b.cli.RunCoderRPC(ctx, task, toRunOpts(opts))
}

// Tools lists every plugin tool the exposure policy admits.
func (b *rpcBackend) Tools() []rpcserve.ToolInfo {
	if b.cli == nil {
		return nil
	}
	tools := b.cli.ListAllRPCTools()
	out := make([]rpcserve.ToolInfo, 0, len(tools))
	for _, t := range tools {
		out = append(out, rpcserve.ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			Usage:       t.Usage,
			Schema:      t.Schema,
			ReadOnly:    t.ReadOnly,
		})
	}
	return out
}

// CallTool invokes any policy-admitted plugin tool by name.
func (b *rpcBackend) CallTool(ctx context.Context, name, args string) (string, error) {
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	return b.cli.RunAnyRPCTool(ctx, name, args)
}

// MCPProxyTools lists the tools re-exported from the MCP servers ChatCLI is
// connected to (rpcserve.MCPToolProxy). ListMCPProxyTools itself waits,
// bounded, for the initial connect pass so an early tools/list doesn't miss
// the aggregated catalog.
func (b *rpcBackend) MCPProxyTools() []rpcserve.MCPProxyToolInfo {
	if b.cli == nil {
		return nil
	}
	tools := b.cli.ListMCPProxyTools()
	out := make([]rpcserve.MCPProxyToolInfo, 0, len(tools))
	for _, t := range tools {
		out = append(out, rpcserve.MCPProxyToolInfo{
			Name:        t.Name,
			Server:      t.Server,
			Description: t.Description,
			InputSchema: t.InputSchema,
			ReadOnly:    t.ReadOnly,
		})
	}
	return out
}

// CallMCPProxyTool forwards a proxied tool call to its origin MCP server.
func (b *rpcBackend) CallMCPProxyTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	return b.cli.RunMCPProxyTool(ctx, name, args)
}

// ManageSession implements rpcserve.SessionBackend: it administers the live
// per-session chat histories (the ask_chatcli session parameter) and bridges
// them to the persistent saved-session store shared with the REPL /session
// command, so an MCP client can save a conversation and pick it up later —
// including from an interactive chatcli.
func (b *rpcBackend) ManageSession(_ context.Context, action, session, name string) (string, error) {
	switch action {
	case "active":
		b.mu.Lock()
		ids := make([]string, 0, len(b.sessions))
		for id := range b.sessions {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		var sb strings.Builder
		for _, id := range ids {
			fmt.Fprintf(&sb, "%s (%d messages)\n", id, len(b.sessions[id]))
		}
		b.mu.Unlock()
		if sb.Len() == 0 {
			return "no live sessions — ask_chatcli creates one per session id", nil
		}
		return strings.TrimSpace(sb.String()), nil

	case "clear":
		b.mu.Lock()
		_, existed := b.sessions[session]
		delete(b.sessions, session)
		b.mu.Unlock()
		if !existed {
			return fmt.Sprintf("session %q had no live history", session), nil
		}
		return fmt.Sprintf("session %q cleared — the next ask_chatcli call starts fresh", session), nil

	case "save":
		b.mu.Lock()
		hist := append([]models.Message(nil), b.sessions[session]...)
		b.mu.Unlock()
		if len(hist) == 0 {
			return "", errCLI(fmt.Sprintf("session %q has no messages to save — talk to ask_chatcli with this session id first", session))
		}
		if name == "" {
			name = session
		}
		if b.store == nil {
			return "", errCLIUnavailable
		}
		if err := b.store.SaveSessionRPC(name, hist); err != nil {
			return "", err
		}
		return fmt.Sprintf("saved session %q (%d messages) to the store as %q", session, len(hist), name), nil

	case "load":
		if name == "" {
			return "", errCLI("name is required for load — which saved session to restore (see action list)")
		}
		if b.store == nil {
			return "", errCLIUnavailable
		}
		hist, err := b.store.LoadSessionRPC(name)
		if err != nil {
			return "", err
		}
		if len(hist) > rpcMaxHistory {
			hist = hist[len(hist)-rpcMaxHistory:]
		}
		b.mu.Lock()
		b.sessions[session] = hist
		b.mu.Unlock()
		return fmt.Sprintf("loaded saved session %q into live session %q (%d messages) — ask_chatcli with this session id continues that conversation", name, session, len(hist)), nil

	case "list":
		if b.store == nil {
			return "", errCLIUnavailable
		}
		names, err := b.store.ListSessionsRPC()
		if err != nil {
			return "", err
		}
		if len(names) == 0 {
			return "the session store is empty", nil
		}
		return strings.Join(names, "\n"), nil

	case "delete":
		if name == "" {
			return "", errCLI("name is required for delete — which saved session to remove (see action list)")
		}
		if b.store == nil {
			return "", errCLIUnavailable
		}
		if err := b.store.DeleteSessionRPC(name); err != nil {
			return "", err
		}
		return fmt.Sprintf("deleted saved session %q", name), nil

	default:
		return "", errCLI(fmt.Sprintf("unknown action %q — use save, load, list, delete, clear or active", action))
	}
}

// Skills serves the installed skill catalog (MCP prompts).
func (b *rpcBackend) Skills() []rpcserve.SkillInfo {
	if b.cli == nil {
		return nil
	}
	skills := b.cli.ListSkillsRPC()
	out := make([]rpcserve.SkillInfo, 0, len(skills))
	for _, s := range skills {
		out = append(out, rpcserve.SkillInfo{Name: s.Name, Description: s.Description})
	}
	return out
}

// SkillContent returns a skill's body for prompts/get.
func (b *rpcBackend) SkillContent(name string) (string, error) {
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	return b.cli.SkillContentRPC(name)
}

// ProvidersJSON describes providers/models for per-call routing.
func (b *rpcBackend) ProvidersJSON() (string, error) {
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	return b.cli.ProvidersRPC()
}

// toRunOpts converts the wire options into the CLI run options.
func toRunOpts(o rpcserve.RunOpts) cli.RPCRunOpts {
	return cli.RPCRunOpts{Provider: o.Provider, Model: o.Model, Quality: o.Quality, Emit: o.Emit}
}

type errCLI string

func (e errCLI) Error() string { return string(e) }

var errCLIUnavailable = errCLI("agent/coder/tools unavailable: ChatCLI failed to initialize")
