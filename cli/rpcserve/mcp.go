/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rpcserve

import (
	"context"
	"encoding/json"
)

// MCPProtocolVersion is the MCP revision this server speaks.
const MCPProtocolVersion = "2024-11-05"

// Backend is the minimal chat capability (used by the ACP server).
type Backend interface {
	Prompt(ctx context.Context, session, text string) (string, error)
}

// ToolInfo describes a built-in tool exposed over MCP.
type ToolInfo struct {
	Name        string
	Description string
}

// MCPBackend is the full capability surface the MCP server exposes: chat,
// the agent and coder loops, and the curated built-in tools. Implemented by
// the CLI so an MCP client can drive ChatCLI's real functionality.
type MCPBackend interface {
	Backend
	Agent(ctx context.Context, session, task string) (string, error)
	Coder(ctx context.Context, session, task string) (string, error)
	BuiltinTools() []ToolInfo
	CallBuiltin(ctx context.Context, name, args string) (string, error)
}

// MCP implements the Model Context Protocol server methods over JSON-RPC.
type MCP struct {
	backend MCPBackend
	name    string
	version string
}

// NewMCP builds an MCP handler.
func NewMCP(backend MCPBackend, name, version string) *MCP {
	return &MCP{backend: backend, name: name, version: version}
}

// Handle dispatches an MCP method. Wire it into Server via the handlerFunc type.
func (m *MCP) Handle(ctx context.Context, method string, params json.RawMessage) (interface{}, *RPCError) {
	switch method {
	case "initialize":
		return map[string]interface{}{
			"protocolVersion": MCPProtocolVersion,
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":      map[string]interface{}{"name": m.name, "version": m.version},
		}, nil
	case "notifications/initialized", "initialized":
		return nil, nil // notification
	case "ping":
		return map[string]interface{}{}, nil
	case "tools/list":
		return map[string]interface{}{"tools": m.toolDefinitions()}, nil
	case "tools/call":
		return m.callTool(ctx, params)
	default:
		return nil, errf(CodeMethodNotFound, "unknown method %q", method)
	}
}

func textArg(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": desc}
}

func objSchema(props map[string]interface{}, required ...string) map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": props, "required": required}
}

// toolDefinitions advertises chat, the agent/coder loops, and each curated
// built-in tool.
func (m *MCP) toolDefinitions() []map[string]interface{} {
	tools := []map[string]interface{}{
		{
			"name":        "ask_chatcli",
			"description": "Ask the model a question (chat, no tools). Keeps a server-side conversation per session.",
			"inputSchema": objSchema(map[string]interface{}{
				"prompt":  textArg("The question or instruction."),
				"session": textArg("Optional conversation id (default: \"mcp\")."),
			}, "prompt"),
		},
		{
			"name":        "agent_task",
			"description": "Run ChatCLI's full agent (ReAct) loop on a task. The agent autonomously uses its built-in tools (read, search, shell via coder, web, memory) and returns the transcript. Use for multi-step work.",
			"inputSchema": objSchema(map[string]interface{}{
				"task":    textArg("The task for the agent to accomplish."),
				"session": textArg("Optional conversation id."),
			}, "task"),
		},
		{
			"name":        "coder_task",
			"description": "Run ChatCLI's coder loop on a task (focused on reading/editing code in the workspace).",
			"inputSchema": objSchema(map[string]interface{}{
				"task":    textArg("The coding task."),
				"session": textArg("Optional conversation id."),
			}, "task"),
		},
	}
	for _, t := range m.backend.BuiltinTools() {
		tools = append(tools, map[string]interface{}{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": objSchema(map[string]interface{}{
				"args": textArg("Arguments for the tool (e.g. a path, query, or JSON envelope)."),
			}, "args"),
		})
	}
	return tools
}

type mcpToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type mcpArgs struct {
	Prompt  string `json:"prompt"`
	Task    string `json:"task"`
	Session string `json:"session"`
	Args    string `json:"args"`
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

	switch p.Name {
	case "ask_chatcli":
		if a.Prompt == "" {
			return nil, errf(CodeInvalidParams, "prompt is required")
		}
		return m.result(m.backend.Prompt(ctx, session, a.Prompt))
	case "agent_task":
		if a.Task == "" {
			return nil, errf(CodeInvalidParams, "task is required")
		}
		return m.result(m.backend.Agent(ctx, session, a.Task))
	case "coder_task":
		if a.Task == "" {
			return nil, errf(CodeInvalidParams, "task is required")
		}
		return m.result(m.backend.Coder(ctx, session, a.Task))
	default:
		// A curated built-in tool.
		return m.result(m.backend.CallBuiltin(ctx, p.Name, a.Args))
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
