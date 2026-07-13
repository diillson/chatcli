/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rpcserve

import (
	"context"
	"encoding/json"
	"strings"
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
	// MCPProxyTools lists the tools re-exported from the MCP servers the
	// backend is connected to; CallMCPProxyTool forwards a call to the
	// origin server with the raw MCP arguments object.
	MCPProxyTools() []MCPProxyToolInfo
	CallMCPProxyTool(ctx context.Context, name string, args json.RawMessage) (string, error)
	Skills() []SkillInfo
	SkillContent(name string) (string, error)
	ProvidersJSON() (string, error)
}

// MCP implements the Model Context Protocol server methods over JSON-RPC.
type MCP struct {
	backend MCPBackend
	name    string
	version string
	notify  func(method string, params interface{}) error
}

// NewMCP builds an MCP handler.
func NewMCP(backend MCPBackend, name, version string) *MCP {
	return &MCP{backend: backend, name: name, version: version}
}

// SetNotifier wires the server's notification writer (call after NewServer)
// so the handler can push notifications/tools/list_changed to the client.
func (m *MCP) SetNotifier(fn func(method string, params interface{}) error) {
	m.notify = fn
}

// NotifyToolsChanged pushes notifications/tools/list_changed to the client.
// Safe to call from any goroutine (the server serializes writes); no-op
// until SetNotifier is wired.
func (m *MCP) NotifyToolsChanged() {
	if m.notify == nil {
		return
	}
	_ = m.notify("notifications/tools/list_changed", map[string]interface{}{})
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
	default:
		return nil, errf(CodeMethodNotFound, "unknown method %q", method)
	}
}

// initialize negotiates the protocol revision and advertises capabilities.
func (m *MCP) initialize(params json.RawMessage) map[string]interface{} {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	version := MCPProtocolVersion
	for _, v := range mcpSupportedVersions {
		if p.ProtocolVersion == v {
			version = v
			break
		}
	}
	return map[string]interface{}{
		"protocolVersion": version,
		"capabilities": map[string]interface{}{
			// tools.listChanged: proxied MCP tools appear as ChatCLI's own
			// MCP client connects to its configured servers (async at
			// startup) and can change on dynamic refresh/disconnect.
			"tools":   map[string]interface{}{"listChanged": true},
			"prompts": map[string]interface{}{"listChanged": false},
		},
		"serverInfo": map[string]interface{}{"name": m.name, "version": m.version},
	}
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
		"session": textArg("Optional conversation id (default: \"mcp\")."),
	}
	for k, v := range routingProps() {
		chatProps[k] = v
	}
	agentProps := map[string]interface{}{
		"task":    textArg("The task for the agent to accomplish."),
		"session": textArg("Optional conversation id."),
		"quality": qualityProp(),
	}
	for k, v := range routingProps() {
		agentProps[k] = v
	}
	coderProps := map[string]interface{}{
		"task":    textArg("The coding task."),
		"session": textArg("Optional conversation id."),
		"quality": qualityProp(),
	}
	for k, v := range routingProps() {
		coderProps[k] = v
	}

	tools := make([]map[string]interface{}, 0, 8)
	if m.backend.HasLLM() {
		tools = append(tools, []map[string]interface{}{
			{
				"name":        "ask_chatcli",
				"description": "Ask the model a question (chat, no tools). Keeps a server-side conversation per session. Supports per-call provider/model routing.",
				"inputSchema": objSchema(chatProps, "prompt"),
				"annotations": map[string]interface{}{"readOnlyHint": true},
			},
			{
				"name": "agent_task",
				"description": "Run ChatCLI's full agent (ReAct) loop on a task. The agent autonomously uses every built-in tool " +
					"(files, shell, web, memory, knowledge, MCP servers ChatCLI is connected to) and returns the transcript. " +
					"Supports per-call provider/model routing and quality-harness toggles (plan, refine, verify, reflexion, convergence, lessons).",
				"inputSchema": objSchema(agentProps, "task"),
				"annotations": map[string]interface{}{"readOnlyHint": false},
			},
			{
				"name":        "coder_task",
				"description": "Run ChatCLI's coder loop on a task (reading/editing code and running commands in the workspace). Supports per-call provider/model routing and quality toggles.",
				"inputSchema": objSchema(coderProps, "task"),
				"annotations": map[string]interface{}{"readOnlyHint": false},
			},
		}...)
	}
	tools = append(tools, map[string]interface{}{
		"name":        "list_providers",
		"description": "List the configured LLM providers, the active provider/model, and each provider's cataloged models — the routing surface for the provider/model parameters.",
		"inputSchema": objSchema(map[string]interface{}{}),
		"annotations": map[string]interface{}{"readOnlyHint": true},
	})
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
	// verbatim — no args-string envelope).
	for _, t := range m.backend.MCPProxyTools() {
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
	Provider string            `json:"provider"`
	Model    string            `json:"model"`
	Quality  map[string]string `json:"quality"`
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
	opts := RunOpts{Provider: a.Provider, Model: a.Model, Quality: a.Quality}

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
	default:
		// Proxied MCP tools carry the caller's arguments object verbatim
		// to the origin server; everything else is a plugin tool taking
		// the args-string envelope.
		if strings.HasPrefix(p.Name, "mcp_") {
			return m.result(m.backend.CallMCPProxyTool(ctx, p.Name, p.Arguments))
		}
		return m.result(m.backend.CallTool(ctx, p.Name, a.Args))
	}
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
