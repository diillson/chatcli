/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rpcserve

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/i18n"
)

// mcpSupportedVersions are the MCP revisions this server can speak, newest
// first. initialize negotiates: if the client requests a known revision the
// server echoes it, otherwise it answers with the newest supported one.
var mcpSupportedVersions = []string{"2025-03-26", "2024-11-05"}

// MCPProtocolVersion is the default (newest) MCP revision this server speaks.
const MCPProtocolVersion = "2025-03-26"

// Backend is the minimal chat capability (used by the ACP server).
type Backend interface {
	Prompt(ctx context.Context, session, text string) (string, error)
}

// RunOpts parameterizes chat/agent/coder calls made over RPC.
type RunOpts struct {
	Provider string
	Model    string
	Quality  map[string]string
	Emit     func(string)
	// Events, when non-nil, installs a structured event sink for the run
	// (ACP structured bridge); it takes precedence over Emit.
	Events agentevents.Sink
	// Plain requests the bare chat passthrough (no memory/contexts/skills
	// enrichment, no compaction) — the pre-parity ask_chatcli behavior, kept
	// as an escape hatch for callers that want a cheap raw LLM turn.
	Plain bool
	// Permissions, when non-nil, lets the run ask the connected client to
	// approve policy-gated actions (coder "ask" rules, dangerous commands)
	// even without a structured event sink — the MCP elicitation bridge.
	// Nil keeps the historical unattended contract.
	Permissions agentevents.PermissionRequester
}

// ToolInfo describes a tool exposed over MCP.
type ToolInfo struct {
	Name        string
	Description string
	Usage       string
	Schema      string
	ReadOnly    bool
}

// SkillInfo describes a skill served as an MCP prompt.
type SkillInfo struct {
	Name        string
	Description string
}

// MCPProxyToolInfo describes a tool proxied from an MCP server the backend
// is itself connected to (ChatCLI as an MCP hub). It is advertised under
// the prefixed name "mcp_<Name>" with its origin JSON Schema intact, and
// calls forward the caller's arguments object verbatim.
type MCPProxyToolInfo struct {
	Name        string // origin tool name, WITHOUT the mcp_ prefix
	Server      string
	Description string
	InputSchema map[string]interface{}
	ReadOnly    bool
}

// MCPBackend is the full capability surface the MCP server exposes: chat
// with per-call routing, the agent/coder loops with quality toggles, every
// policy-admitted plugin tool, the skill catalog (prompts), and provider
// discovery.
type MCPBackend interface {
	Backend
	// HasLLM reports whether at least one LLM provider is configured in
	// this ChatCLI instance. Without one, the LLM-backed harness tools
	// (ask_chatcli / agent_task / coder_task) are hidden from tools/list —
	// every direct tool keeps working with the caller's own model.
	HasLLM() bool
	PromptWith(ctx context.Context, session, text string, opts RunOpts) (string, error)
	Agent(ctx context.Context, session, task string, opts RunOpts) (string, error)
	Coder(ctx context.Context, session, task string, opts RunOpts) (string, error)
	Tools() []ToolInfo
	CallTool(ctx context.Context, name, args string) (string, error)
	Skills() []SkillInfo
	SkillContent(name string) (string, error)
	ProvidersJSON() (string, error)
}

// MCPToolProxy is the optional backend capability for the MCP-hub surface:
// re-exporting tools from MCP servers the backend is itself connected to.
// Kept out of MCPBackend so existing implementations stay compatible —
// the handler upgrades via type assertion (the io.ReaderFrom pattern).
// MCPProxyTools lists the re-exported tools; CallMCPProxyTool forwards a
// call to the origin server with the caller's raw MCP arguments object.
type MCPToolProxy interface {
	MCPProxyTools() []MCPProxyToolInfo
	CallMCPProxyTool(ctx context.Context, name string, args json.RawMessage) (string, error)
}

// ToolsListChangedMethod is the MCP notification a server pushes when its
// tool catalog changes. Callers wire it to Server.Notify when the backend's
// proxied catalog is dynamic (servers connecting, refreshes, disconnects).
const ToolsListChangedMethod = "notifications/tools/list_changed"

// SessionBackend is the optional backend capability for managing chat
// sessions over MCP: the live per-session histories behind ask_chatcli and
// the persistent saved-session store (the same store the REPL /session
// command uses). Optional for the same compatibility reason as
// MCPToolProxy — the handler upgrades via type assertion.
type SessionBackend interface {
	// ManageSession executes one session action (save/load/list/delete/
	// clear/active) and returns a human-readable outcome.
	ManageSession(ctx context.Context, action, session, name string) (string, error)
}

// SessionSearchBackend is the optional backend capability for the additive
// manage_session actions that need parameters beyond (action, session, name):
// full-text search across the saved-session store and forking a saved
// session. Kept separate from SessionBackend so existing implementations
// stay source-compatible — the handler upgrades via type assertion, the same
// pattern as MCPToolProxy.
type SessionSearchBackend interface {
	// SearchSessions runs a full-text search over the saved-session store.
	SearchSessions(ctx context.Context, query string) (string, error)
	// ForkSession copies saved session source under the new name target.
	ForkSession(ctx context.Context, source, target string) (string, error)
}

// ResourceInfo describes one resource exposed over MCP resources/list.
type ResourceInfo struct {
	URI         string
	Name        string
	Description string
	MimeType    string
}

// ResourceContent is the payload returned by resources/read.
type ResourceContent struct {
	URI      string
	MimeType string
	Text     string
}

// ResourceBackend is the optional backend capability for the MCP resources
// surface: read-only exports of ChatCLI's local state (user memory, contexts,
// knowledge bases, skills, saved sessions) under chatcli:// URIs. Optional
// for the same compatibility reason as MCPToolProxy — the handler upgrades
// via type assertion and advertises the capability only when present.
type ResourceBackend interface {
	Resources() []ResourceInfo
	ReadResource(ctx context.Context, uri string) (ResourceContent, error)
}

// MCP implements the Model Context Protocol server methods over JSON-RPC.
type MCP struct {
	backend MCPBackend
	name    string
	version string
	// rt holds the mutable runtime wiring (server requester, client-declared
	// capabilities). Kept behind a pointer so the exported struct stays
	// comparable — a mutex or func field inline would be an incompatible
	// API change.
	rt *mcpRuntime
}

// mcpRuntime is the per-connection mutable state: initialize and tools/call
// dispatch on concurrent goroutines, so access is mutex-guarded.
type mcpRuntime struct {
	mu sync.Mutex
	// request is the server→client round-trip (Server.Request), used for
	// elicitation/create. Wired by SetRequester after NewServer.
	request func(ctx context.Context, method string, params interface{}) (json.RawMessage, error)
	// elicits records whether the client declared the elicitation
	// capability during initialize.
	elicits bool
}

// NewMCP builds an MCP handler.
func NewMCP(backend MCPBackend, name, version string) *MCP {
	return &MCP{backend: backend, name: name, version: version, rt: &mcpRuntime{}}
}

// SetRequester wires the server's client-request round-trip (call after
// NewServer). Without it, permission elicitation stays off.
func (m *MCP) SetRequester(fn func(ctx context.Context, method string, params interface{}) (json.RawMessage, error)) {
	m.rt.mu.Lock()
	defer m.rt.mu.Unlock()
	m.rt.request = fn
}

// clientElicits reports whether the connected client declared the
// elicitation capability during initialize.
func (m *MCP) clientElicits() bool {
	m.rt.mu.Lock()
	defer m.rt.mu.Unlock()
	return m.rt.elicits
}

// Handle dispatches an MCP method. Wire it into Server via the handlerFunc type.
func (m *MCP) Handle(ctx context.Context, method string, params json.RawMessage) (interface{}, *RPCError) {
	switch method {
	case "initialize":
		return m.initialize(params), nil
	case "notifications/initialized", "initialized":
		return nil, nil // notification
	case "ping":
		return map[string]interface{}{}, nil
	case "tools/list":
		return map[string]interface{}{"tools": m.toolDefinitions()}, nil
	case "tools/call":
		return m.callTool(ctx, params)
	case "prompts/list":
		return map[string]interface{}{"prompts": m.promptDefinitions()}, nil
	case "prompts/get":
		return m.getPrompt(params)
	case "resources/list":
		return m.listResources()
	case "resources/read":
		return m.readResource(ctx, params)
	default:
		return nil, errf(CodeMethodNotFound, "unknown method %q", method)
	}
}

// initialize negotiates the protocol revision and advertises capabilities.
func (m *MCP) initialize(params json.RawMessage) map[string]interface{} {
	var p struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
	}
	_ = json.Unmarshal(params, &p)
	// A client that declares elicitation understands elicitation/create
	// regardless of the negotiated revision string; anything else (wrapped
	// CLIs, older clients) never receives a server→client request.
	_, hasElicit := p.Capabilities["elicitation"]
	m.rt.mu.Lock()
	m.rt.elicits = hasElicit
	m.rt.mu.Unlock()
	version := MCPProtocolVersion
	for _, v := range mcpSupportedVersions {
		if p.ProtocolVersion == v {
			version = v
			break
		}
	}
	// tools.listChanged is advertised only when the backend proxies MCP
	// tools: that catalog is dynamic (ChatCLI's own client connects async
	// at startup; servers refresh and disconnect). The native surface is
	// static.
	_, hasProxy := m.backend.(MCPToolProxy)
	capabilities := map[string]interface{}{
		"tools":   map[string]interface{}{"listChanged": hasProxy},
		"prompts": map[string]interface{}{"listChanged": false},
	}
	// Resources are advertised only when the backend exports them (read-only
	// chatcli:// state: memory, contexts, knowledge, skills, sessions).
	if _, ok := m.backend.(ResourceBackend); ok {
		capabilities["resources"] = map[string]interface{}{"subscribe": false, "listChanged": false}
	}
	return map[string]interface{}{
		"protocolVersion": version,
		"capabilities":    capabilities,
		"serverInfo":      map[string]interface{}{"name": m.name, "version": m.version},
	}
}

// listResources serves resources/list from the optional ResourceBackend.
func (m *MCP) listResources() (interface{}, *RPCError) {
	rb, ok := m.backend.(ResourceBackend)
	if !ok {
		return nil, errf(CodeMethodNotFound, "this server exposes no resources")
	}
	infos := rb.Resources()
	out := make([]map[string]interface{}, 0, len(infos))
	for _, r := range infos {
		mime := r.MimeType
		if mime == "" {
			mime = "text/plain"
		}
		out = append(out, map[string]interface{}{
			"uri":         r.URI,
			"name":        r.Name,
			"description": r.Description,
			"mimeType":    mime,
		})
	}
	return map[string]interface{}{"resources": out}, nil
}

// readResource serves resources/read from the optional ResourceBackend.
func (m *MCP) readResource(ctx context.Context, params json.RawMessage) (interface{}, *RPCError) {
	rb, ok := m.backend.(ResourceBackend)
	if !ok {
		return nil, errf(CodeMethodNotFound, "this server exposes no resources")
	}
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.URI == "" {
		return nil, errf(CodeInvalidParams, "uri is required")
	}
	content, err := rb.ReadResource(ctx, p.URI)
	if err != nil {
		return nil, errf(CodeInvalidParams, "cannot read %q: %v", p.URI, err)
	}
	mime := content.MimeType
	if mime == "" {
		mime = "text/plain"
	}
	return map[string]interface{}{
		"contents": []map[string]interface{}{
			{"uri": content.URI, "mimeType": mime, "text": content.Text},
		},
	}, nil
}

func textArg(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": desc}
}

func objSchema(props map[string]interface{}, required ...string) map[string]interface{} {
	// A nil variadic slice marshals as JSON null, and MCP clients (Claude)
	// validate required as an array — always emit [] when empty.
	if required == nil {
		required = []string{}
	}
	return map[string]interface{}{"type": "object", "properties": props, "required": required}
}

// routingProps are the shared per-call routing/quality parameters accepted
// by chat/agent/coder.
func routingProps() map[string]interface{} {
	return map[string]interface{}{
		"provider": textArg("Optional LLM provider override for this call (see list_providers)."),
		"model":    textArg("Optional model override for this call."),
	}
}

func qualityProp() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"description": "Optional quality-harness toggles for this run, as CHATCLI_QUALITY_* env overrides. " +
			"Examples: {\"CHATCLI_QUALITY_ENABLED\":\"true\"}, refine/verify/reflexion/plan/convergence knobs.",
		"additionalProperties": map[string]interface{}{"type": "string"},
	}
}

// toolDefinitions advertises the harness tools plus every policy-admitted
// plugin tool with its capability annotations.
func (m *MCP) toolDefinitions() []map[string]interface{} {
	chatProps := map[string]interface{}{
		"prompt":  textArg("The question or instruction."),
		"session": textArg("Optional conversation id (default: \"mcp\"). Also selects which /context attachments apply — pass \"default\" to see the interactive REPL's default session contexts."),
		"plain": map[string]interface{}{
			"type":        "boolean",
			"description": "Skip the ChatCLI experience pipeline and send the prompt raw (cheaper, no memory/contexts/skills).",
		},
	}
	for k, v := range routingProps() {
		chatProps[k] = v
	}
	agentProps := map[string]interface{}{
		"task":    textArg("The task for the agent to accomplish."),
		"session": textArg("Optional session id — scopes which /context attachments and knowledge bases the run sees."),
		"quality": qualityProp(),
	}
	for k, v := range routingProps() {
		agentProps[k] = v
	}
	coderProps := map[string]interface{}{
		"task":    textArg("The coding task."),
		"session": textArg("Optional session id — scopes which /context attachments and knowledge bases the run sees."),
		"quality": qualityProp(),
	}
	for k, v := range routingProps() {
		coderProps[k] = v
	}

	tools := make([]map[string]interface{}, 0, 8)
	if m.backend.HasLLM() {
		tools = append(tools, []map[string]interface{}{
			{
				"name": "ask_chatcli",
				"description": "Chat with ChatCLI's FULL experience pipeline: the user's long-term memory and profile, " +
					"/context attachments for the session, pinned and trigger-activated skills, knowledge retrieval, " +
					"and token-aware history compaction — the same enrichment an interactive ChatCLI turn gets. " +
					"Keeps a server-side conversation per session. Supports per-call provider/model routing; " +
					"pass plain=true for a raw passthrough without enrichment.",
				"inputSchema": objSchema(chatProps, "prompt"),
				"annotations": map[string]interface{}{"readOnlyHint": true},
			},
			{
				"name": "coder_task",
				"description": "Run ChatCLI's full agent loop on a task — the RECOMMENDED tool for anything that needs autonomous work. " +
					"It reads/edits files, runs commands, and uses every built-in tool (web, memory, knowledge, MCP servers ChatCLI is connected to), then returns the transcript. " +
					"Supports per-call provider/model routing and quality-harness toggles (plan, refine, verify, reflexion, convergence, lessons).",
				"inputSchema": objSchema(coderProps, "task"),
				"annotations": map[string]interface{}{"readOnlyHint": false},
			},
			{
				"name": "agent_task",
				"description": "Run ChatCLI's command-oriented agent (ReAct) loop, which works by proposing shell commands step by step. " +
					"Prefer coder_task for general work; use this only when the caller specifically wants the command-suggestion style loop. " +
					"Supports per-call provider/model routing and quality-harness toggles (plan, refine, verify, reflexion, convergence, lessons).",
				"inputSchema": objSchema(agentProps, "task"),
				"annotations": map[string]interface{}{"readOnlyHint": false},
			},
		}...)
	}
	tools = append(tools, map[string]interface{}{
		"name":        "list_providers",
		"description": "List the configured LLM providers, the active provider/model, and each provider's models — live from the provider API when it supports listing, merged with the static catalog (models_source tells which). The routing surface for the provider/model parameters.",
		"inputSchema": objSchema(map[string]interface{}{}),
		"annotations": map[string]interface{}{"readOnlyHint": true},
	})
	if _, ok := m.backend.(SessionBackend); ok {
		props := map[string]interface{}{
			"action":  textArg("One of: save, load, attach, detach, status, list, delete, clear, active, policy_mode" + m.sessionSearchActions() + "."),
			"session": textArg("Live session id (the ask_chatcli session parameter; default \"mcp\"). Used by save, load, attach, detach, status, clear."),
			"name":    textArg("Saved-session name in the store. Required for load, attach and delete; defaults to the session id for save. For fork: the SOURCE saved session. For policy_mode: auto | interactive | status."),
		}
		desc := "Manage chat sessions: persist and restore the server-side conversations behind ask_chatcli's session parameter, and administer the saved-session store shared with ChatCLI's /session command. " +
			"Actions: save (persist a live session's history under a name and bind to it), load (restore a saved session into a live session id, continuing that conversation with write-through), " +
			"attach (bind a live session to a saved session — created on first turn if missing — so turns write through and writes from other surfaces are adopted), " +
			"detach (drop the binding), status (binding and message count), " +
			"list (saved sessions in the store), delete (remove a saved session), clear (reset a live session), active (live session ids with message counts), " +
			"policy_mode (session security-policy mode for agent/coder runs: name=auto makes coder policy ask rules auto-approve, name=interactive restores prompting, empty name reports the current mode; deny rules and safety-immune operations always keep gating)."
		if _, ok := m.backend.(SessionSearchBackend); ok {
			props["query"] = textArg("Full-text query across all saved sessions. Required for search.")
			props["to"] = textArg("Target name for fork (the new saved-session copy). Required for fork.")
			desc += " Plus: search (full-text search across saved sessions), fork (copy saved session `name` to `to`)."
		}
		tools = append(tools, map[string]interface{}{
			"name":        "manage_session",
			"description": desc,
			"inputSchema": objSchema(props, "action"),
			"annotations": map[string]interface{}{"readOnlyHint": false},
		})
	}
	for _, t := range m.backend.Tools() {
		desc := t.Description
		if u := strings.TrimSpace(t.Usage); u != "" {
			desc += "\n\nUsage:\n" + u
		}
		tools = append(tools, map[string]interface{}{
			"name":        t.Name,
			"description": desc,
			"inputSchema": objSchema(map[string]interface{}{
				"args": textArg("Arguments for the tool — a flat string or the JSON envelope the usage describes."),
			}),
			"annotations": map[string]interface{}{"readOnlyHint": t.ReadOnly},
		})
	}
	// Proxied MCP tools: re-exported from the servers this ChatCLI is
	// connected to, under the same mcp_<tool> names the agent loop uses,
	// with the origin server's own JSON Schema (arguments pass through
	// verbatim — no args-string envelope). Only when the backend has the
	// hub capability.
	proxy, hasProxy := m.backend.(MCPToolProxy)
	if !hasProxy {
		return tools
	}
	for _, t := range proxy.MCPProxyTools() {
		schema := t.InputSchema
		if schema == nil {
			schema = objSchema(map[string]interface{}{})
		}
		tools = append(tools, map[string]interface{}{
			"name":        "mcp_" + t.Name,
			"description": "[MCP:" + t.Server + "] " + t.Description,
			"inputSchema": schema,
			"annotations": map[string]interface{}{"readOnlyHint": t.ReadOnly},
		})
	}
	return tools
}

// promptDefinitions serves the installed skill catalog as MCP prompts.
func (m *MCP) promptDefinitions() []map[string]interface{} {
	skills := m.backend.Skills()
	out := make([]map[string]interface{}, 0, len(skills))
	for _, s := range skills {
		out = append(out, map[string]interface{}{
			"name":        s.Name,
			"description": s.Description,
		})
	}
	return out
}

func (m *MCP) getPrompt(params json.RawMessage) (interface{}, *RPCError) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return nil, errf(CodeInvalidParams, "prompt name is required")
	}
	content, err := m.backend.SkillContent(p.Name)
	if err != nil {
		return nil, errf(CodeInvalidParams, "unknown prompt %q: %v", p.Name, err)
	}
	return map[string]interface{}{
		"messages": []map[string]interface{}{
			{
				"role":    "user",
				"content": map[string]interface{}{"type": "text", "text": content},
			},
		},
	}, nil
}

type mcpToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type mcpArgs struct {
	Prompt   string            `json:"prompt"`
	Task     string            `json:"task"`
	Session  string            `json:"session"`
	Args     string            `json:"args"`
	Action   string            `json:"action"`
	Name     string            `json:"name"`
	Query    string            `json:"query"`
	To       string            `json:"to"`
	Provider string            `json:"provider"`
	Model    string            `json:"model"`
	Quality  map[string]string `json:"quality"`
	Plain    bool              `json:"plain"`
}

func (m *MCP) callTool(ctx context.Context, params json.RawMessage) (interface{}, *RPCError) {
	var p mcpToolCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, errf(CodeInvalidParams, "invalid params: %v", err)
	}
	var a mcpArgs
	_ = json.Unmarshal(p.Arguments, &a)
	session := a.Session
	if session == "" {
		session = "mcp"
	}
	opts := RunOpts{Provider: a.Provider, Model: a.Model, Quality: a.Quality, Plain: a.Plain,
		Permissions: m.permissionsFor(ctx)}

	switch p.Name {
	case "ask_chatcli":
		if a.Prompt == "" {
			return nil, errf(CodeInvalidParams, "prompt is required")
		}
		return m.result(m.backend.PromptWith(ctx, session, a.Prompt, opts))
	case "agent_task":
		if a.Task == "" {
			return nil, errf(CodeInvalidParams, "task is required")
		}
		return m.result(m.backend.Agent(ctx, session, a.Task, opts))
	case "coder_task":
		if a.Task == "" {
			return nil, errf(CodeInvalidParams, "task is required")
		}
		return m.result(m.backend.Coder(ctx, session, a.Task, opts))
	case "list_providers":
		return m.result(m.backend.ProvidersJSON())
	case "manage_session":
		if sb, ok := m.backend.(SessionBackend); ok {
			if a.Action == "" {
				return nil, errf(CodeInvalidParams, "action is required (save, load, attach, detach, status, list, delete, clear, active, policy_mode%s)", m.sessionSearchActions())
			}
			if ssb, ok := m.backend.(SessionSearchBackend); ok {
				switch a.Action {
				case "search":
					if a.Query == "" {
						return nil, errf(CodeInvalidParams, "query is required for search")
					}
					return m.result(ssb.SearchSessions(ctx, a.Query))
				case "fork":
					if a.Name == "" || a.To == "" {
						return nil, errf(CodeInvalidParams, "fork requires name (source saved session) and to (target name)")
					}
					return m.result(ssb.ForkSession(ctx, a.Name, a.To))
				}
			}
			return m.result(sb.ManageSession(ctx, a.Action, session, a.Name))
		}
		// No session capability: fall through to the plugin dispatcher,
		// which reports the tool as unknown.
		return m.result(m.backend.CallTool(ctx, p.Name, a.Args))
	default:
		// Proxied MCP tools carry the caller's arguments object verbatim
		// to the origin server; everything else is a plugin tool taking
		// the args-string envelope.
		if proxy, ok := m.backend.(MCPToolProxy); ok && strings.HasPrefix(p.Name, "mcp_") {
			return m.result(proxy.CallMCPProxyTool(ctx, p.Name, p.Arguments))
		}
		return m.result(m.backend.CallTool(ctx, p.Name, a.Args))
	}
}

// permissionsFor returns a PermissionRequester bridging approval prompts to
// MCP elicitation/create, scoped to one tools/call context. Nil when the
// client did not declare the elicitation capability, the requester is not
// wired, or CHATCLI_MCP_ELICITATION=off — the run then keeps the historical
// unattended contract. The kill switch exists for clients that declare the
// capability without actually rendering dialogs: every gated action would
// otherwise stall for the permission timeout and deny.
func (m *MCP) permissionsFor(ctx context.Context) agentevents.PermissionRequester {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_MCP_ELICITATION"))) {
	case "off", "false", "0", "no", "disabled":
		return nil
	}
	m.rt.mu.Lock()
	request, elicits := m.rt.request, m.rt.elicits
	m.rt.mu.Unlock()
	if request == nil || !elicits {
		return nil
	}
	return &mcpElicitRequester{ctx: ctx, request: request, timeout: mcpPermissionTimeout()}
}

// mcpPermissionTimeoutDefault bounds how long one permission round-trip may
// wait for the human — generous enough to notice a dialog and decide, while
// still guaranteeing the run never hangs forever on a client that swallowed
// the request. Caveat for MCP clients that enforce their own tools/call
// timeout shorter than this: the client kills the whole call first, so tune
// CHATCLI_MCP_PERMISSION_TIMEOUT below the client's limit to keep the run
// alive. ACP clients (IDEs) hold the prompt turn open, so the long default
// is safe there.
const mcpPermissionTimeoutDefault = 600 * time.Second

// mcpPermissionTimeoutDisabledCeiling is what "off"/"0" resolve to. A wait
// with no bound at all left the turn pinned forever when a client kept the
// pipe open but died without EOF (no dialog, no cancel, no disconnect) —
// 24h is unlimited for any human decision while guaranteeing the run
// eventually frees its resources (and, process-wide, the capture slot that
// serializes RPC runs).
const mcpPermissionTimeoutDisabledCeiling = 24 * time.Hour

// mcpPermissionTimeout resolves CHATCLI_MCP_PERMISSION_TIMEOUT: a Go
// duration ("90s", "2m") or plain seconds; "0"/"off" lift the bound to a
// 24h ceiling (effectively "wait for the human", never "wait forever").
// Unset or invalid keeps the default. Like CHATCLI_MCP_TOOLS, the knob
// governs both RPC surfaces: MCP elicitation/create and ACP
// session/request_permission (acpSink).
func mcpPermissionTimeout() time.Duration {
	v := strings.TrimSpace(os.Getenv("CHATCLI_MCP_PERMISSION_TIMEOUT"))
	if v == "" {
		return mcpPermissionTimeoutDefault
	}
	if strings.EqualFold(v, "off") {
		return mcpPermissionTimeoutDisabledCeiling
	}
	if d, err := time.ParseDuration(v); err == nil && d >= 0 {
		if d == 0 {
			return mcpPermissionTimeoutDisabledCeiling
		}
		return d
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		if secs == 0 {
			return mcpPermissionTimeoutDisabledCeiling
		}
		return time.Duration(secs) * time.Second
	}
	return mcpPermissionTimeoutDefault
}

// mcpElicitRequester implements agentevents.PermissionRequester and
// agentevents.PermissionDecider over MCP elicitation. The boolean surface
// keeps today's single "approve" checkbox; the decision surface offers the
// terminal's four-way vocabulary as a string enum when the caller can
// persist the choice (OfferAlways). Anything but an explicit accept with a
// recognized value denies; a client answering "method not found" maps to
// ErrPermissionUnsupported (legacy fallback).
type mcpElicitRequester struct {
	ctx     context.Context
	request func(ctx context.Context, method string, params interface{}) (json.RawMessage, error)
	// timeout bounds each round-trip; 0 waits until ctx dies (legacy).
	timeout time.Duration
	// dialogDead flips after a timeout: the client demonstrably does not
	// answer elicitation, so later requests in this run fail fast instead of
	// stalling the loop for another timeout per gated action. The agent loop
	// is sequential, so no locking is needed.
	dialogDead bool
}

var _ agentevents.PermissionRequester = (*mcpElicitRequester)(nil)
var _ agentevents.PermissionDecider = (*mcpElicitRequester)(nil)

// mcpDecisionValues is the elicitation enum, ordered like the terminal
// prompt's choices. Values match agentevents.PermissionDecision verbatim.
var mcpDecisionValues = []string{
	string(agentevents.PermissionAllowOnce),
	string(agentevents.PermissionAllowAlways),
	string(agentevents.PermissionDenyOnce),
	string(agentevents.PermissionDenyAlways),
}

func (e *mcpElicitRequester) RequestPermission(tc agentevents.ToolCall, reason string) (bool, error) {
	d, err := e.RequestPermissionDecision(agentevents.PermissionRequest{Tool: tc, Reason: reason})
	return d.Allowed(), err
}

func (e *mcpElicitRequester) RequestPermissionDecision(req agentevents.PermissionRequest) (agentevents.PermissionDecision, error) {
	action := strings.TrimSpace(req.Tool.Title)
	if action == "" {
		action = req.Tool.Name
	}
	msg := "ChatCLI requests permission to run: " + action
	if strings.TrimSpace(req.Reason) != "" {
		msg += "\n" + req.Reason
	}
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"approve": map[string]interface{}{
				"type":        "boolean",
				"description": "Approve running this action?",
			},
		},
		"required": []string{"approve"},
	}
	if req.OfferAlways {
		schema = map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"decision": map[string]interface{}{
					"type":        "string",
					"description": i18n.T("mcp.permission.decision_desc"),
					"enum":        mcpDecisionValues,
					"enumNames": []string{
						i18n.T("acp.permission.allow_once"),
						i18n.T("acp.permission.allow_always"),
						i18n.T("acp.permission.reject_once"),
						i18n.T("acp.permission.reject_always"),
					},
				},
			},
			"required": []string{"decision"},
		}
	}
	if e.dialogDead {
		return agentevents.PermissionDenyOnce, agentevents.ErrPermissionTimeout
	}
	ctx, cancel := e.ctx, func() {}
	if e.timeout > 0 {
		ctx, cancel = context.WithTimeout(e.ctx, e.timeout)
	}
	raw, err := e.request(ctx, "elicitation/create", map[string]interface{}{
		"message":         msg,
		"requestedSchema": schema,
	})
	cancel()
	if err != nil {
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) && rpcErr.Code == CodeMethodNotFound {
			return agentevents.PermissionDenyOnce, agentevents.ErrPermissionUnsupported
		}
		// Our own bound expired while the run's context is still alive: the
		// dialog is unanswered (likely never rendered). Deny fail-safe and
		// mark it dead so the run doesn't stall again on every gated action.
		if errors.Is(err, context.DeadlineExceeded) && e.ctx.Err() == nil {
			e.dialogDead = true
			return agentevents.PermissionDenyOnce, agentevents.ErrPermissionTimeout
		}
		return agentevents.PermissionDenyOnce, err
	}
	var resp struct {
		Action  string `json:"action"`
		Content struct {
			Approve  bool   `json:"approve"`
			Decision string `json:"decision"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return agentevents.PermissionDenyOnce, err
	}
	if resp.Action != "accept" {
		return agentevents.PermissionDenyOnce, nil
	}
	// Lenient response handling: prefer the typed decision, fall back to the
	// legacy approve boolean for clients that render the old form. Anything
	// unrecognized denies fail-safe.
	if v := strings.ToLower(strings.TrimSpace(resp.Content.Decision)); v != "" {
		d := agentevents.PermissionDecision(v)
		switch d {
		case agentevents.PermissionAllowOnce, agentevents.PermissionAllowAlways,
			agentevents.PermissionDenyOnce, agentevents.PermissionDenyAlways:
			// A persistent choice the form never offered degrades to its
			// once-only variant — the client cannot widen the caller's scope.
			if !req.OfferAlways && d.Persistent() {
				if d.Allowed() {
					return agentevents.PermissionAllowOnce, nil
				}
				return agentevents.PermissionDenyOnce, nil
			}
			return d, nil
		}
		return agentevents.PermissionDenyOnce, nil
	}
	if resp.Content.Approve {
		return agentevents.PermissionAllowOnce, nil
	}
	return agentevents.PermissionDenyOnce, nil
}

// sessionSearchActions returns the action-list suffix for the search/fork
// capability, empty when the backend does not implement it.
func (m *MCP) sessionSearchActions() string {
	if _, ok := m.backend.(SessionSearchBackend); ok {
		return ", search, fork"
	}
	return ""
}

// result wraps a backend outcome in the MCP tool-result shape, reporting
// errors in-band per the MCP convention.
func (m *MCP) result(text string, err error) (interface{}, *RPCError) {
	if err != nil {
		return map[string]interface{}{
			"content": []map[string]interface{}{{"type": "text", "text": "error: " + err.Error()}},
			"isError": true,
		}, nil
	}
	return map[string]interface{}{
		"content": []map[string]interface{}{{"type": "text", "text": text}},
	}, nil
}
