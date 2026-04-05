package mcp

import "time"

// ServerConfig represents a configured MCP server.
type ServerConfig struct {
	Name      string            `json:"name"`
	Command   string            `json:"command"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Transport TransportType     `json:"transport"`
	URL       string            `json:"url,omitempty"` // For SSE transport
	Enabled   bool              `json:"enabled"`
	// Overrides lists built-in plugin names that this MCP server replaces.
	// When the server is connected, these built-ins are shadowed (hidden from the LLM).
	// If the server disconnects, the built-ins are automatically restored.
	// Example: ["@webfetch", "@websearch"]
	Overrides []string `json:"overrides,omitempty"`
}

// TransportType defines MCP transport mechanism.
type TransportType string

const (
	TransportStdio TransportType = "stdio"
	TransportSSE   TransportType = "sse"
)

// MCPTool represents a tool discovered from an MCP server.
type MCPTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"inputSchema"`
	ServerName  string                 `json:"-"`
}

// MCPToolResult is the result of executing an MCP tool.
type MCPToolResult struct {
	Content  string `json:"content"`
	IsError  bool   `json:"isError,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// ServerStatus tracks the health of an MCP server connection.
type ServerStatus struct {
	Name      string
	Connected bool
	ToolCount int
	LastPing  time.Time
	LastError error
	StartedAt time.Time
}
