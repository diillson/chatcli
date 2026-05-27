package rpcserve

import (
	"context"
	"encoding/json"
)

// MCPProtocolVersion is the MCP revision this server speaks.
const MCPProtocolVersion = "2024-11-05"

// Backend is the capability the protocol servers expose: run a prompt for a
// session and return the reply. Provided by the CLI, backed by the LLM/agent.
type Backend interface {
	Prompt(ctx context.Context, session, text string) (string, error)
}

// MCP implements the Model Context Protocol server methods over JSON-RPC.
// It advertises ChatCLI as a tool provider so any MCP client can call it.
type MCP struct {
	backend Backend
	name    string
	version string
}

// NewMCP builds an MCP handler.
func NewMCP(backend Backend, name, version string) *MCP {
	return &MCP{backend: backend, name: name, version: version}
}

// Handle dispatches an MCP method. Wire it into Server via the Handler type.
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
		return map[string]interface{}{"tools": mcpToolDefinitions()}, nil
	case "tools/call":
		return m.callTool(ctx, params)
	default:
		return nil, Errf(CodeMethodNotFound, "unknown method %q", method)
	}
}

// mcpToolDefinitions is the tool catalog advertised to clients.
func mcpToolDefinitions() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"name":        "ask_chatcli",
			"description": "Send a prompt to ChatCLI and get the model's answer. Maintains a server-side conversation per session.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"prompt":  map[string]interface{}{"type": "string", "description": "The question or instruction."},
					"session": map[string]interface{}{"type": "string", "description": "Optional conversation id (default: \"mcp\")."},
				},
				"required": []string{"prompt"},
			},
		},
	}
}

type mcpToolCallParams struct {
	Name      string `json:"name"`
	Arguments struct {
		Prompt  string `json:"prompt"`
		Session string `json:"session"`
	} `json:"arguments"`
}

func (m *MCP) callTool(ctx context.Context, params json.RawMessage) (interface{}, *RPCError) {
	var p mcpToolCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, Errf(CodeInvalidParams, "invalid params: %v", err)
	}
	if p.Name != "ask_chatcli" {
		return nil, Errf(CodeInvalidParams, "unknown tool %q", p.Name)
	}
	if p.Arguments.Prompt == "" {
		return nil, Errf(CodeInvalidParams, "prompt is required")
	}
	session := p.Arguments.Session
	if session == "" {
		session = "mcp"
	}

	reply, err := m.backend.Prompt(ctx, session, p.Arguments.Prompt)
	if err != nil {
		// MCP convention: tool errors are reported in-band with isError.
		return map[string]interface{}{
			"content": []map[string]interface{}{{"type": "text", "text": "error: " + err.Error()}},
			"isError": true,
		}, nil
	}
	return map[string]interface{}{
		"content": []map[string]interface{}{{"type": "text", "text": reply}},
	}, nil
}
