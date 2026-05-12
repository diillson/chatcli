/*
 * ChatCLI - MCP type definitions
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Wire-level data types for the MCP package: server config schema,
 * transport selector, discovered tool, tool-call result, and server
 * status. JSON marshaling helpers, validation, and the catch-all
 * Extensions plumbing live in config.go to keep this file focused on
 * the data shapes themselves.
 */
package mcp

import (
	"encoding/json"
	"time"
)

// ServerConfig represents a configured MCP server.
//
// The schema is intentionally close to the de facto conventions
// established by the broader MCP ecosystem (Claude Code, Cline,
// Cursor, AWS EKS MCP, etc.). Fields documented as Tier 1/2 below
// were promoted from "third-party servers use it" to "chatcli acts
// on it" because each one has a clear runtime effect; everything
// else is preserved verbatim via Extensions for round-trip fidelity.
type ServerConfig struct {
	// --- Core transport (always required) ---

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

	// --- Tier 1: typed extensions with direct runtime behavior ---

	// Description is shown by /mcp status and may be appended to the
	// per-server tools catalog so the LLM knows what the server is for.
	Description string `json:"description,omitempty"`

	// Cwd sets the working directory for stdio transports. Supports
	// ${VAR}/$VAR expansion plus a leading "~/" expanded against the
	// current user's home. Ignored for SSE.
	Cwd string `json:"cwd,omitempty"`

	// AutoApprove lists tool names that bypass the agent-mode approval
	// gate. "*" matches every tool exposed by this server. Tool names
	// are matched both with and without the "mcp_" prefix the agent uses
	// internally, so the user can write either form.
	AutoApprove []string `json:"autoApprove,omitempty"`

	// AlwaysAllow is an alias of AutoApprove popularized by Cline. The
	// two are merged into the same runtime set so users can copy configs
	// without renaming the key.
	AlwaysAllow []string `json:"alwaysAllow,omitempty"`

	// DisabledTools hides specific tools from the LLM by name. Useful
	// when a server exposes 30+ tools but the workflow only needs a
	// handful. Saves tokens on every turn.
	DisabledTools []string `json:"disabledTools,omitempty"`

	// Timeout caps how long a single MCP RPC waits for a response, in
	// seconds. Zero falls back to the package default (DefaultRequestTimeout).
	Timeout int `json:"timeout,omitempty"`

	// --- Tier 2: typed extensions with more design surface ---

	// InitTimeout caps how long the MCP initialize handshake (and the
	// SSE endpoint-event wait) is allowed to take, in seconds. Zero
	// falls back to the package default (DefaultInitializeTimeout).
	InitTimeout int `json:"initTimeout,omitempty"`

	// Headers are extra HTTP headers attached to every SSE request and
	// every JSON-RPC POST. Values support ${VAR}/$VAR expansion.
	Headers map[string]string `json:"headers,omitempty"`

	// Auth declares HTTP authentication for SSE transports. See AuthConfig.
	Auth *AuthConfig `json:"auth,omitempty"`

	// EnabledTools is an allowlist that takes precedence over DisabledTools.
	// When non-empty, ONLY listed tools are exposed; everything else is hidden.
	EnabledTools []string `json:"enabledTools,omitempty"`

	// Tags label the server for filtering in /mcp list.
	Tags []string `json:"tags,omitempty"`

	// Category is a single-string classification (e.g. "aws", "database",
	// "search") shown next to the server in /mcp list.
	Category string `json:"category,omitempty"`

	// Trust auto-approves every tool from this server without consulting
	// AutoApprove. Reserved for servers the operator has independently
	// vetted — the gate will log every Trust=true call so audit trails
	// stay clean.
	Trust bool `json:"trust,omitempty"`

	// --- Tier 3: catch-all for unknown keys ---

	// Extensions preserves every JSON key that the typed fields above
	// do not cover, so an mcp_servers.json round-tripped through
	// chatcli keeps third-party annotations intact. Populated by
	// UnmarshalJSON; re-emitted by MarshalJSON. Never overwrites a
	// known field on the way out.
	Extensions map[string]json.RawMessage `json:"-"`
}

// AuthConfig declares HTTP authentication for SSE transports.
//
// Supported Types — each maps to a specific Authorization header
// shape so the wire format stays predictable across servers that may
// have read the spec differently:
//
//	"bearer" — Authorization: Bearer <Token>
//	"basic"  — Authorization: Basic base64(Username:Password)
//	"header" — <Header>: <Token>   (defaults to X-API-Key when Header is empty)
//
// An empty Type disables auth even when other fields are populated,
// so a half-completed config does not leak credentials.
//
// Token, Username, Password, and Header all support ${VAR}/$VAR
// expansion against the parent process environment so secrets can
// live in the shell instead of plaintext in mcp_servers.json.
type AuthConfig struct {
	Type     string `json:"type,omitempty"`
	Token    string `json:"token,omitempty"`
	Header   string `json:"header,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
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
	Starting  bool // true while the server is being launched in background
	ToolCount int
	LastPing  time.Time
	LastError error
	StartedAt time.Time
}
