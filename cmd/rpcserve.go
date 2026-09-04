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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

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
	// The surface is the server actually running: labeling an
	// `mcp-server` run as "acp" mislabels its audit trail and telemetry.
	chatCLI.SetAuditSurface(kind)
	if chatCLI != nil {
		// stdin carries the JSON-RPC protocol: any interactive confirmation
		// would consume protocol frames and hang the run. Unattended mode
		// auto-approves every prompt at the source (same regime as the
		// gateway daemon); CHATCLI_MCP_DANGER=block re-arms the dangerous-
		// command gate as an in-band refusal instead of a stdin read.
		chatCLI.SetUnattended(true)
		chatCLI.SetRPCDangerPolicy(strings.EqualFold(os.Getenv("CHATCLI_MCP_DANGER"), "block"))

		// Cross-channel continuity: join the shared conversation hub in
		// RESUME mode (adopting the REPL/gateway thread, never rotating it)
		// so a conversation started in ChatCLI continues from any MCP
		// client. Gated by the hub's own enablement plus the MCP-specific
		// kill switch; CHATCLI_MCP_HUB_PRINCIPAL isolates the MCP thread.
		if !strings.EqualFold(os.Getenv("CHATCLI_MCP_HUB"), "off") {
			if closeHub := chatCLI.StartHubResume(context.Background(), os.Getenv("CHATCLI_MCP_HUB_PRINCIPAL")); closeHub != nil {
				defer closeHub()
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
		// Follow every manager rebuild (project overlay, `/config reload`).
		chatCLI.OnManagerRebuild(backend.setManager)
		// Same bounded lifecycle the REPL applies on start: expire
		// machine-created sessions (autosaves, mcp- mirrors) past their TTL.
		// Background — boot never waits on disk.
		go chatCLI.CleanExpiredMachineSessionsRPC()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	if chatCLI != nil {
		// MCP/ACP servers exit with their client: persist spend and release
		// paid caches like every other surface.
		defer chatCLI.FinalizeSpend(context.WithoutCancel(ctx))
	}
	defer stop()

	ver := version.GetCurrentVersion().Version
	switch kind {
	case "acp":
		a := rpcserve.NewACP(backend, ver)
		srv := rpcserve.NewServer(os.Stdin, os.Stdout, a.Handle)
		defer quarantineStdout(logger)()
		a.SetNotifier(srv.Notify)
		a.SetRequester(srv.Request)
		logger.Info("acp: serving over stdio")
		return srv.Serve(ctx)
	default: // mcp
		m := rpcserve.NewMCP(backend, "chatcli", ver)
		srv := rpcserve.NewServer(os.Stdin, os.Stdout, m.Handle)
		defer quarantineStdout(logger)()
		// Permission elicitation: server→client approval prompts for
		// policy-gated actions, used only when the client declared the
		// elicitation capability at initialize.
		m.SetRequester(srv.Request)
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
	PruneSessionsRPC(prefix string, keep int) int
	SessionModTimeRPC(name string) (time.Time, error)
	SessionExistsRPC(name string) bool
}

// rpcBackend implements rpcserve.MCPBackend (and thus Backend). Chat keeps a
// per-session history; agent/coder/tools delegate to the ChatCLI.
type rpcBackend struct {
	// mgr is swapped when a project .env unlocks providers (AdoptWorkspace),
	// so every read goes through manager() under mgrMu — JSON-RPC requests
	// are served concurrently.
	mgrMu    sync.RWMutex
	mgr      manager.LLMManager
	cli      *cli.ChatCLI
	store    sessionStore // nil when ChatCLI failed to initialize
	provider string
	model    string

	mu       sync.Mutex
	sessions map[string][]models.Message
	// bindings maps a live session id to the named saved session it is bound
	// to (cross-surface continuity); bindSync holds the store mtime as of our
	// last load/save of that name (the refresh watermark). Lazily initialized
	// by bindSession. Both guarded by mu.
	bindings map[string]string
	bindSync map[string]time.Time
}

// rpcMaxHistory is the legacy hard message cap, applied only on the plain
// passthrough path (no compactor there). The full-pipeline path uses ChatCLI's
// token-aware history compaction instead. CHATCLI_MCP_MAX_HISTORY overrides
// the cap for both paths (0 keeps the default behavior).
const rpcMaxHistory = 30

// historyCap resolves the effective message cap for the given path.
// full=true: 0 means "no hard cap" (the compactor bounds the history).
// full=false (plain): 0 means the legacy rpcMaxHistory.
func historyCap(full bool) int {
	if v := strings.TrimSpace(os.Getenv("CHATCLI_MCP_MAX_HISTORY")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	if full {
		return 0
	}
	return rpcMaxHistory
}

// capHistory trims hist to the last n messages; n<=0 means no cap. The cut
// is pair-aware at both ends: a leading tool result whose assistant call was
// cut off, or a trailing assistant tool call whose results never landed
// (cancelled run), would make native tool-use APIs reject the whole request
// when the history is replayed — so both are trimmed.
func capHistory(hist []models.Message, n int) []models.Message {
	if n > 0 && len(hist) > n {
		hist = hist[len(hist)-n:]
	}
	return trimDanglingToolPairs(hist)
}

// trimDanglingToolPairs drops orphaned halves of tool-call exchanges from
// the history's edges (never from the middle — interior pairs are intact).
func trimDanglingToolPairs(hist []models.Message) []models.Message {
	start, end := 0, len(hist)
	for start < end && hist[start].Role == "tool" {
		start++
	}
	for end > start {
		last := hist[end-1]
		if last.Role == "assistant" && len(last.ToolCalls) > 0 {
			end--
			continue
		}
		break
	}
	return hist[start:end]
}

// manager returns the live LLM manager.
func (b *rpcBackend) manager() manager.LLMManager {
	b.mgrMu.RLock()
	defer b.mgrMu.RUnlock()
	return b.mgr
}

// setManager adopts a rebuilt manager (project overlay, or a `/config
// reload` issued from the client): without it the server would keep serving
// the provider set the configuration had before the reload.
func (b *rpcBackend) setManager(m manager.LLMManager) {
	if m == nil {
		return
	}
	b.mgrMu.Lock()
	b.mgr = m
	b.mgrMu.Unlock()
}

// AdoptWorkspace implements rpcserve.ACPWorkspaceBackend: the editor told us
// which project it opened, so layer that project's .env over the process
// configuration (fill-only, once per process).
//
// This is also the recovery path for the environment an editor never passes
// on: a client that spawns `chatcli acp` without the user's shell gets no
// CHATCLI_DOTENV and no exported provider variables, and the project file is
// then the only per-repository configuration ChatCLI can still see.
func (b *rpcBackend) AdoptWorkspace(dir string) {
	if b.cli == nil {
		return
	}
	// The rebuilt manager arrives through the OnManagerRebuild hook, so the
	// swap happens under the backend's own lock.
	b.cli.ApplyProjectEnv(dir)
}

// HasLLM reports whether any LLM provider is configured. The MCP server
// hides the LLM-backed harness tools when this is false.
func (b *rpcBackend) HasLLM() bool {
	return len(b.manager().GetAvailableProviders()) > 0
}

// errNoLLM is the actionable no-provider error, sent in-band to the caller
// (a model): it explains what still works and how to enable the rest.
var errNoLLM = errCLI("no LLM provider is configured in this ChatCLI instance, so chat/agent/coder tools are unavailable. " +
	"Every direct tool (read, search, web, memory, knowledge, …) keeps working with your own model. " +
	"To enable the harness tools, configure a provider in ChatCLI (an API key env var or 'chatcli' + '/auth login <provider>') and restart the server.")

// Prompt implements the chat capability with per-session history.
func (b *rpcBackend) Prompt(ctx context.Context, session, text string) (string, error) {
	return b.PromptWith(ctx, session, text, rpcserve.RunOpts{})
}

// PromptWith is chat with optional per-call provider/model routing. With a
// full ChatCLI available it runs the SAME enrichment pipeline as an
// interactive turn (user memory, /context attachments for the session,
// pinned + trigger-activated skills, knowledge retrieval, token-aware
// compaction); opts.Plain — or a degraded init — falls back to the bare
// passthrough.
func (b *rpcBackend) PromptWith(ctx context.Context, session, text string, opts rpcserve.RunOpts) (string, error) {
	if !b.HasLLM() {
		return "", errNoLLM
	}

	// Adopt writes another surface made to the bound saved session (no-op
	// for unbound sessions) before snapshotting the live history.
	b.refreshBound(session)
	b.mu.Lock()
	hist := append([]models.Message(nil), b.sessions[session]...)
	b.mu.Unlock()

	if b.cli != nil && !opts.Plain {
		turn, err := b.cli.RunChatTurnRPC(ctx, session, text, hist,
			cli.RPCChatOpts{Provider: opts.Provider, Model: opts.Model})
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

	return b.promptPlain(ctx, session, text, hist, opts)
}

// promptPlain is the bare passthrough chat path: raw per-session history,
// no enrichment, legacy message cap. Kept for opts.Plain and for the
// degraded mode where ChatCLI failed to initialize.
func (b *rpcBackend) promptPlain(ctx context.Context, session, text string, hist []models.Message, opts rpcserve.RunOpts) (string, error) {
	provider := opts.Provider
	if provider == "" {
		provider = b.provider
	}
	model := opts.Model
	if model == "" {
		model = b.model
	}
	client, err := b.manager().GetClient(provider, model)
	if err != nil {
		return "", err
	}

	hist = append(hist, models.Message{Role: "user", Content: text})
	reply, err := client.SendPrompt(ctx, text, hist, 0)
	if err != nil {
		return "", err
	}
	hist = append(hist, models.Message{Role: "assistant", Content: reply})

	b.mu.Lock()
	capped := capHistory(hist, historyCap(false))
	b.sessions[session] = capped
	b.mu.Unlock()
	// Same persistence contract as the full-pipeline path: plain passthrough
	// conversations are sessions too.
	b.autosaveSession(session, capped)
	b.writeThrough(session, capped)
	return reply, nil
}

// mcpAutosaveKeepDefault bounds how many distinct mcp- session mirrors
// survive pruning. Mirrors are rolling (one file per session id, updated in
// place), so this counts distinct MCP conversations, not turns. Like the
// REPL autosaves, retention is primarily the 90-day TTL; the count is a
// generous backstop, overridable via CHATCLI_SESSION_AUTOSAVE_KEEP.
const mcpAutosaveKeepDefault = 600

// mcpAutosaveKeep resolves the mirror keep-count (shared env with the REPL
// autosave backstop so operators tune one knob).
func mcpAutosaveKeep() int {
	if v := strings.TrimSpace(os.Getenv("CHATCLI_SESSION_AUTOSAVE_KEEP")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return mcpAutosaveKeepDefault
}

// mcpSessionAutosaveEnabled resolves the MCP autosave gate. An explicit
// CHATCLI_MCP_SESSION_AUTOSAVE always wins; otherwise the global
// CHATCLI_SESSION_AUTOSAVE gate applies (default ON) — the same default the
// interactive REPL uses, so both surfaces persist conversations unless the
// operator opts out.
func mcpSessionAutosaveEnabled() bool {
	if v := strings.TrimSpace(os.Getenv("CHATCLI_MCP_SESSION_AUTOSAVE")); v != "" {
		switch strings.ToLower(v) {
		case "false", "0", "off", "no", "disabled":
			return false
		}
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_SESSION_AUTOSAVE"))) {
	case "false", "0", "off", "no", "disabled":
		return false
	}
	return true
}

// autosaveSession persists the live session to the saved-session store as a
// rolling "mcp-<session>" mirror (one file per session id, updated in place),
// then prunes the oldest mirrors past the keep-count. Trivial histories
// (fewer than 2 non-system messages) are skipped. Best-effort: a store
// failure never fails the turn.
func (b *rpcBackend) autosaveSession(session string, hist []models.Message) {
	if b.store == nil || !mcpSessionAutosaveEnabled() {
		return
	}
	nonSystem := 0
	for _, m := range hist {
		if m.Role != "system" {
			nonSystem++
		}
	}
	if nonSystem < 2 {
		return
	}
	if b.store.SaveSessionRPC("mcp-"+session, hist) == nil {
		b.store.PruneSessionsRPC("mcp-", mcpAutosaveKeep())
	}
}

// Agent runs the full agent (ReAct) loop with per-call options, scoped to
// the caller's session: contexts/knowledge AND the per-session conversation
// (runLoopSession swaps the history in and persists the update).
func (b *rpcBackend) Agent(ctx context.Context, session, task string, opts rpcserve.RunOpts) (string, error) {
	if !b.HasLLM() {
		return "", errNoLLM
	}
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	return b.runLoopSession(session, func(o cli.RPCRunOpts) (string, error) {
		return b.cli.RunAgentRPC(ctx, task, o)
	}, opts)
}

// Coder runs the coder loop with per-call options, scoped to the caller's
// session (contexts/knowledge + per-session conversation).
func (b *rpcBackend) Coder(ctx context.Context, session, task string, opts rpcserve.RunOpts) (string, error) {
	if !b.HasLLM() {
		return "", errNoLLM
	}
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	return b.runLoopSession(session, func(o cli.RPCRunOpts) (string, error) {
		return b.cli.RunCoderRPC(ctx, task, o)
	}, opts)
}

// AgentStream / CoderStream are the ACP streaming variants.
func (b *rpcBackend) AgentStream(ctx context.Context, session, task string, opts rpcserve.RunOpts) (string, error) {
	return b.Agent(ctx, session, task, opts)
}

func (b *rpcBackend) CoderStream(ctx context.Context, session, task string, opts rpcserve.RunOpts) (string, error) {
	return b.Coder(ctx, session, task, opts)
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
// managePolicyMode implements the manage_session policy_mode action: the
// session-scoped security-policy mode (the /policy command). Deny rules,
// the command validator and safety-immune operations keep gating in
// automode; only "ask" verdicts auto-approve.
func (b *rpcBackend) managePolicyMode(name string) (string, error) {
	if b.cli == nil {
		return "", fmt.Errorf("policy_mode requires the full ChatCLI runtime (degraded server mode)")
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "status":
		// fall through to the status report below
	case "auto", "automode", "on":
		b.cli.SetPolicyAutoMode(true)
	case "interactive", "ask", "manual", "off":
		b.cli.SetPolicyAutoMode(false)
	default:
		return "", fmt.Errorf("unknown policy mode %q — use auto or interactive", name)
	}
	if b.cli.PolicyAutoMode() {
		return "policy mode: auto — coder policy ask rules auto-approve; deny rules and safety-immune operations still gate. Not persisted; new sessions start interactive.", nil
	}
	return "policy mode: interactive — coder policy ask rules prompt for approval.", nil
}

func (b *rpcBackend) ManageSession(_ context.Context, action, session, name string) (string, error) {
	switch action {
	case "policy_mode":
		return b.managePolicyMode(name)

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
		hist, existed := b.sessions[session]
		delete(b.sessions, session)
		b.mu.Unlock()
		b.unbindSession(session)
		if !existed {
			return fmt.Sprintf("session %q had no live history", session), nil
		}
		// Last-chance autosave: clearing must never be the moment a
		// conversation silently ceases to exist.
		b.autosaveSession(session, hist)
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
		b.bindSession(session, name)
		return fmt.Sprintf("saved session %q (%d messages) to the store as %q — the live session is now bound to it (turns keep it updated)", session, len(hist), name), nil

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
		// Full-pipeline turns compact token-aware, so a whole saved session
		// can be restored; CHATCLI_MCP_MAX_HISTORY still bounds it if set.
		hist = capHistory(hist, historyCap(b.cli != nil))
		b.mu.Lock()
		b.sessions[session] = hist
		b.mu.Unlock()
		b.bindSession(session, name)
		return fmt.Sprintf("loaded saved session %q into live session %q (%d messages) — ask_chatcli with this session id continues that conversation, and turns are written back to %q (cross-surface continuity; use detach to stop)", name, session, len(hist), name), nil

	case "attach":
		return b.manageAttach(session, name)

	case "detach":
		return b.manageDetach(session)

	case "status":
		return b.manageStatus(session)

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
		return "", errCLI(fmt.Sprintf("unknown action %q — use save, load, attach, detach, status, list, delete, clear or active", action))
	}
}

// manageAttach implements the attach action: bind a live session to a saved
// session, hydrating from the store when the file exists. A name not in the
// store keeps the live conversation as the seed — the first write-through
// creates the file from it; dropping it here would destroy the caller's
// in-flight context.
func (b *rpcBackend) manageAttach(session, name string) (string, error) {
	if name == "" {
		return "", errCLI("name is required for attach — which saved session to bind this live session to")
	}
	if b.store == nil {
		return "", errCLIUnavailable
	}
	if b.store.SessionExistsRPC(name) {
		hist, err := b.store.LoadSessionRPC(name)
		if err != nil {
			return "", err
		}
		hist = capHistory(hist, historyCap(b.cli != nil))
		b.mu.Lock()
		b.sessions[session] = hist
		b.mu.Unlock()
	}
	b.bindSession(session, name)
	return fmt.Sprintf("live session %q is now bound to saved session %q: every turn is written through, and writes from other surfaces (REPL, gateway, another server) are adopted before each turn", session, name), nil
}

// manageDetach implements the detach action.
func (b *rpcBackend) manageDetach(session string) (string, error) {
	prev := b.boundName(session)
	if prev == "" {
		return fmt.Sprintf("live session %q has no binding", session), nil
	}
	b.unbindSession(session)
	return fmt.Sprintf("live session %q detached from %q — turns stay in memory only (plus the autosave mirror)", session, prev), nil
}

// manageStatus implements the status action.
func (b *rpcBackend) manageStatus(session string) (string, error) {
	b.mu.Lock()
	count := len(b.sessions[session])
	b.mu.Unlock()
	if name := b.boundName(session); name != "" {
		return fmt.Sprintf("live session %q: %d messages, bound to saved session %q (write-through on)", session, count, name), nil
	}
	return fmt.Sprintf("live session %q: %d messages, not bound to any saved session", session, count), nil
}

// SearchSessions implements rpcserve.SessionSearchBackend: full-text search
// across the saved-session store.
func (b *rpcBackend) SearchSessions(_ context.Context, query string) (string, error) {
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	return b.cli.SearchSessionsRPC(query)
}

// ForkSession implements rpcserve.SessionSearchBackend: copy a saved session
// under a new name.
func (b *rpcBackend) ForkSession(_ context.Context, source, target string) (string, error) {
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	if err := b.cli.ForkSessionRPC(source, target); err != nil {
		return "", err
	}
	return fmt.Sprintf("forked saved session %q to %q", source, target), nil
}

// Resources implements rpcserve.ResourceBackend: read-only chatcli:// state.
func (b *rpcBackend) Resources() []rpcserve.ResourceInfo {
	if b.cli == nil {
		return nil
	}
	infos := b.cli.ListRPCResources()
	out := make([]rpcserve.ResourceInfo, 0, len(infos))
	for _, r := range infos {
		out = append(out, rpcserve.ResourceInfo{URI: r.URI, Name: r.Name, Description: r.Description, MimeType: r.MimeType})
	}
	return out
}

// ReadResource implements rpcserve.ResourceBackend.
func (b *rpcBackend) ReadResource(ctx context.Context, uri string) (rpcserve.ResourceContent, error) {
	if b.cli == nil {
		return rpcserve.ResourceContent{}, errCLIUnavailable
	}
	c, err := b.cli.ReadRPCResource(ctx, uri)
	if err != nil {
		return rpcserve.ResourceContent{}, err
	}
	return rpcserve.ResourceContent{URI: c.URI, MimeType: c.MimeType, Text: c.Text}, nil
}

// Skills serves the installed skill catalog (MCP prompts).
func (b *rpcBackend) Skills() []rpcserve.SkillInfo {
	if b.cli == nil {
		return nil
	}
	skills := b.cli.ListSkillsRPC()
	out := make([]rpcserve.SkillInfo, 0, len(skills))
	for _, s := range skills {
		out = append(out, rpcserve.SkillInfo{Name: s.Name, Description: s.Description, ArgumentHint: s.ArgumentHint})
	}
	return out
}

// CommandPrompts implements rpcserve.CommandPrompter: the slash-command
// catalog served through the MCP prompts primitive.
func (b *rpcBackend) CommandPrompts() []rpcserve.CommandPromptInfo {
	if b.cli == nil {
		return nil
	}
	list := b.cli.ListCommandPromptsRPC()
	out := make([]rpcserve.CommandPromptInfo, 0, len(list))
	for _, c := range list {
		out = append(out, rpcserve.CommandPromptInfo{Name: c[0], Description: c[1], ArgumentHint: c[2]})
	}
	return out
}

// CommandPromptRender implements rpcserve.CommandPrompter.
func (b *rpcBackend) CommandPromptRender(ctx context.Context, name, args string) (string, bool) {
	if b.cli == nil {
		return "", false
	}
	return b.cli.RenderCommandPromptRPC(ctx, name, args)
}

// ExpandSlashCommand implements rpcserve.SlashCommandExpander for ACP
// prompt-side template expansion.
func (b *rpcBackend) ExpandSlashCommand(ctx context.Context, _ string, text string) (string, bool) {
	if b.cli == nil {
		return "", false
	}
	return b.cli.ExpandSlashCommandRPC(ctx, text)
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
func toRunOpts(session string, o rpcserve.RunOpts) cli.RPCRunOpts {
	return cli.RPCRunOpts{Provider: o.Provider, Model: o.Model, Quality: o.Quality, Emit: o.Emit, Events: o.Events, Permissions: o.Permissions, Session: session}
}

// ACPCommands lists the slash commands advertised to ACP clients.
func (b *rpcBackend) ACPCommands() []rpcserve.CommandInfo {
	if b.cli == nil {
		return nil
	}
	infos := b.cli.ListACPCommands()
	out := make([]rpcserve.CommandInfo, 0, len(infos))
	for _, c := range infos {
		out = append(out, rpcserve.CommandInfo{Name: c.Name, Description: c.Description, InputHint: c.InputHint})
	}
	return out
}

// RunCommand executes one allowlisted slash command headless for ACP.
// /session and /newsession are intercepted here and served per-session: the
// REPL handlers they would otherwise reach mutate process-global state, which
// the per-session chat path either discards (chat mode swaps history per
// turn) or leaks across every ACP session (agent/coder modes).
func (b *rpcBackend) RunCommand(ctx context.Context, session, line string) (string, error) {
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	fields := strings.Fields(line)
	if len(fields) > 0 {
		switch fields[0] {
		case "/session":
			return b.runSessionCommand(ctx, session, fields[1:])
		case "/newsession":
			return b.runSessionCommand(ctx, session, []string{"new"})
		}
	}
	return b.cli.RunSlashCommandRPC(ctx, line)
}

type errCLI string

func (e errCLI) Error() string { return string(e) }

var errCLIUnavailable = errCLI("agent/coder/tools unavailable: ChatCLI failed to initialize")
