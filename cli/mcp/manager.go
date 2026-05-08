package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// Manager manages MCP server connections and tool routing.
type Manager struct {
	servers  map[string]*ServerConnection
	tools    map[string]*MCPTool // tool name -> tool
	channels *ChannelManager     // handles push messages from servers
	mu       sync.RWMutex
	logger   *zap.Logger
}

// ServerConnection represents an active MCP server.
type ServerConnection struct {
	Config    ServerConfig
	Status    ServerStatus
	Process   *os.Process
	transport mcpTransport // real transport (stdio or SSE)
}

// NewManager creates a new MCP manager.
func NewManager(logger *zap.Logger) *Manager {
	return &Manager{
		servers:  make(map[string]*ServerConnection),
		tools:    make(map[string]*MCPTool),
		channels: NewChannelManager(logger),
		logger:   logger,
	}
}

// Channels returns the channel manager for push message handling.
func (m *Manager) Channels() *ChannelManager {
	return m.channels
}

// LoadConfig loads MCP server configurations from a JSON file.
func (m *Manager) LoadConfig(configPath string) error {
	data, err := os.ReadFile(configPath) //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No config is fine
		}
		return fmt.Errorf("reading MCP config: %w", err)
	}

	var configs struct {
		Servers []ServerConfig `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &configs); err != nil {
		return fmt.Errorf("parsing MCP config: %w", err)
	}

	for _, cfg := range configs.Servers {
		if cfg.Enabled {
			m.mu.Lock()
			m.servers[cfg.Name] = &ServerConnection{
				Config: cfg,
				Status: ServerStatus{Name: cfg.Name, Starting: true},
			}
			m.mu.Unlock()
		}
	}

	m.logger.Info("MCP servers loaded", zap.Int("count", len(m.servers)))
	return nil
}

// DefaultConfigPath returns the default MCP config file path.
func DefaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".chatcli", "mcp_servers.json")
}

// StartAll starts all configured MCP servers.
//
// Failures of individual servers are logged and recorded on
// ServerStatus.LastError, but they never abort the loop — the manager
// always returns nil so a single broken server does not poison the rest
// of the session. Inspect /mcp status (or GetServerStatus) to see which
// servers came up and which did not.
//
// The connection map is snapshotted under m.mu and iterated unlocked
// so startServer (and the discoverTools call inside it, which takes
// m.mu.Lock) can run without deadlocking against this loop and so that
// /mcp status — which also takes m.mu — stays responsive while
// servers are coming up in the background.
func (m *Manager) StartAll(ctx context.Context) error {
	m.mu.RLock()
	pending := make([]*ServerConnection, 0, len(m.servers))
	for _, conn := range m.servers {
		pending = append(pending, conn)
	}
	m.mu.RUnlock()

	for _, conn := range pending {
		if err := m.startServer(ctx, conn); err != nil {
			m.logger.Warn("failed to start MCP server",
				zap.String("server", conn.Config.Name),
				zap.Error(err))
			m.recordStartFailure(conn, err)
		}
	}
	return nil
}

// recordStartFailure stamps a failed start onto conn.Status under
// the manager's write lock so concurrent GetServerStatus callers
// don't race against the post-failure update. Used by StartAll and
// Reload — the success path (set inside startStdioServer / startSSEServer)
// is single-writer per server and not contended.
func (m *Manager) recordStartFailure(conn *ServerConnection, err error) {
	m.mu.Lock()
	conn.Status.LastError = err
	conn.Status.Connected = false
	conn.Status.Starting = false
	m.mu.Unlock()
}

// Reload reconciles the live server set with the on-disk config so
// edits to mcp_servers.json take effect without restarting chatcli.
//
// Diff semantics:
//   - Server present in file but not running   → start it.
//   - Server running but no longer in file     → stop and forget.
//   - Server in both, config bytes equal       → leave alone.
//   - Server in both, config bytes differ      → stop, replace, start.
//
// `enabled: false` is treated as "not in file" — disabling a server
// in the JSON stops it on the next reload.
//
// Returns the set of changes applied, empty when nothing changed.
type ReloadDiff struct {
	Started []string
	Stopped []string
	Updated []string
}

func (m *Manager) Reload(ctx context.Context, configPath string) (ReloadDiff, error) {
	data, err := os.ReadFile(configPath) //#nosec G304 -- same trust boundary as LoadConfig
	if err != nil {
		if os.IsNotExist(err) {
			// File deleted: stop everything we have.
			diff := ReloadDiff{}
			m.mu.RLock()
			for name := range m.servers {
				diff.Stopped = append(diff.Stopped, name)
			}
			m.mu.RUnlock()
			m.StopAll()
			m.mu.Lock()
			m.servers = make(map[string]*ServerConnection)
			m.tools = make(map[string]*MCPTool)
			m.mu.Unlock()
			return diff, nil
		}
		return ReloadDiff{}, fmt.Errorf("reading MCP config: %w", err)
	}

	var parsed struct {
		Servers []ServerConfig `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ReloadDiff{}, fmt.Errorf("parsing MCP config: %w", err)
	}

	desired := make(map[string]ServerConfig)
	for _, cfg := range parsed.Servers {
		if cfg.Enabled {
			desired[cfg.Name] = cfg
		}
	}

	var diff ReloadDiff
	var toStop []string
	var toStart []*ServerConnection
	var toReplace []*ServerConnection

	m.mu.Lock()
	for name := range m.servers {
		if _, keep := desired[name]; !keep {
			toStop = append(toStop, name)
			diff.Stopped = append(diff.Stopped, name)
		}
	}
	for name, cfg := range desired {
		existing, present := m.servers[name]
		if !present {
			conn := &ServerConnection{
				Config: cfg,
				Status: ServerStatus{Name: name, Starting: true},
			}
			m.servers[name] = conn
			toStart = append(toStart, conn)
			diff.Started = append(diff.Started, name)
			continue
		}
		if !serverConfigsEqual(existing.Config, cfg) {
			toStop = append(toStop, name)
			conn := &ServerConnection{
				Config: cfg,
				Status: ServerStatus{Name: name, Starting: true},
			}
			toReplace = append(toReplace, conn)
			diff.Updated = append(diff.Updated, name)
		}
	}
	m.mu.Unlock()

	for _, name := range toStop {
		m.stopOne(name)
	}
	m.mu.Lock()
	for _, conn := range toReplace {
		m.servers[conn.Config.Name] = conn
		toStart = append(toStart, conn)
	}
	m.mu.Unlock()

	for _, conn := range toStart {
		if err := m.startServer(ctx, conn); err != nil {
			m.logger.Warn("failed to start MCP server during reload",
				zap.String("server", conn.Config.Name),
				zap.Error(err))
			m.recordStartFailure(conn, err)
		}
	}

	if len(diff.Started)+len(diff.Stopped)+len(diff.Updated) > 0 {
		m.logger.Info("MCP config reloaded",
			zap.Strings("started", diff.Started),
			zap.Strings("stopped", diff.Stopped),
			zap.Strings("updated", diff.Updated))
	}
	return diff, nil
}

// stopOne stops a single server, removes its tools and drops it from
// the map. Used by Reload; safe to call without holding m.mu.
func (m *Manager) stopOne(name string) {
	m.mu.Lock()
	conn, ok := m.servers[name]
	if !ok {
		m.mu.Unlock()
		return
	}
	transport := conn.transport
	process := conn.Process
	conn.transport = nil
	conn.Process = nil
	conn.Status.Connected = false
	conn.Status.Starting = false
	for tn, t := range m.tools {
		if t.ServerName == name {
			delete(m.tools, tn)
		}
	}
	delete(m.servers, name)
	m.mu.Unlock()

	if transport != nil {
		_ = transport.Close()
	}
	if process != nil {
		_ = process.Kill()
	}
	m.logger.Info("MCP server stopped (reload)", zap.String("server", name))
}

// serverConfigsEqual compares two ServerConfig values for byte-equal
// JSON. Used by Reload to decide whether a server entry has changed
// enough to warrant a restart.
func serverConfigsEqual(a, b ServerConfig) bool {
	aj, errA := json.Marshal(a)
	bj, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(aj) == string(bj)
}

// StopAll stops all running MCP servers.
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, conn := range m.servers {
		if conn.transport != nil {
			m.logger.Info("stopping MCP server", zap.String("server", name))
			_ = conn.transport.Close()
			conn.transport = nil
		}
		if conn.Process != nil {
			_ = conn.Process.Kill()
			conn.Process = nil
		}
		conn.Status.Connected = false
		conn.Status.Starting = false
	}
}

// GetTools returns all discovered MCP tools as ToolDefinitions.
func (m *Manager) GetTools() []models.ToolDefinition {
	m.mu.RLock()
	defer m.mu.RUnlock()

	defs := make([]models.ToolDefinition, 0, len(m.tools))
	for _, tool := range m.tools {
		defs = append(defs, models.ToolDefinition{
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "mcp_" + tool.Name,
				Description: fmt.Sprintf("[MCP:%s] %s", tool.ServerName, tool.Description),
				Parameters:  tool.Parameters,
			},
		})
	}
	return defs
}

// GetToolsSummary returns lightweight tool descriptions (name + description only).
// This saves tokens in the system prompt by deferring full schemas until invocation.
func (m *Manager) GetToolsSummary() []models.ToolDefinition {
	m.mu.RLock()
	defer m.mu.RUnlock()

	defs := make([]models.ToolDefinition, 0, len(m.tools))
	for _, tool := range m.tools {
		defs = append(defs, models.ToolDefinition{
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "mcp_" + tool.Name,
				Description: fmt.Sprintf("[MCP:%s] %s", tool.ServerName, tool.Description),
				// Parameters intentionally omitted — deferred until invocation
			},
		})
	}
	return defs
}

// GetToolSchema returns the full JSON schema for a specific MCP tool.
// Used when the model attempts to invoke a tool and needs parameter details.
func (m *Manager) GetToolSchema(toolName string) map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if tool, ok := m.tools[toolName]; ok {
		return tool.Parameters
	}
	return nil
}

// ToolCount returns the number of discovered MCP tools.
func (m *Manager) ToolCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tools)
}

// ExecuteTool executes an MCP tool by name.
func (m *Manager) ExecuteTool(ctx context.Context, toolName string, args map[string]interface{}) (*MCPToolResult, error) {
	m.mu.RLock()
	tool, ok := m.tools[toolName]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("MCP tool %q not found", toolName)
	}

	m.mu.RLock()
	conn, ok := m.servers[tool.ServerName]
	m.mu.RUnlock()

	if !ok || !conn.Status.Connected {
		return nil, fmt.Errorf("MCP server %q not connected", tool.ServerName)
	}

	return m.callTool(ctx, conn, tool.Name, args)
}

// GetServerStatus returns status for all servers.
func (m *Manager) GetServerStatus() []ServerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	statuses := make([]ServerStatus, 0, len(m.servers))
	for _, conn := range m.servers {
		statuses = append(statuses, conn.Status)
	}
	return statuses
}

// IsMCPTool checks if a tool name is an MCP tool.
func (m *Manager) IsMCPTool(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.tools[name]
	return ok
}

// GetShadowedBuiltins returns the list of built-in plugin names that should be
// hidden because a connected MCP server declares them in its "overrides" field.
// Only overrides from currently connected servers are returned — if a server
// disconnects, its overrides are automatically released and the built-ins become
// visible to the LLM again.
func (m *Manager) GetShadowedBuiltins() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	seen := make(map[string]struct{})
	var shadowed []string
	for _, conn := range m.servers {
		if !conn.Status.Connected {
			continue
		}
		for _, name := range conn.Config.Overrides {
			if _, exists := seen[name]; !exists {
				seen[name] = struct{}{}
				shadowed = append(shadowed, name)
			}
		}
	}
	return shadowed
}

// markDisconnected updates the named server's status to reflect that
// its transport has died (process crash, EPIPE, EOF). Tools owned by
// that server are also dropped so the agent prompt and native tool
// list stop advertising calls that would fail. Safe to call from a
// transport callback goroutine.
func (m *Manager) markDisconnected(name string, reason error) {
	m.mu.Lock()
	conn, ok := m.servers[name]
	if !ok {
		m.mu.Unlock()
		return
	}
	if !conn.Status.Connected && !conn.Status.Starting {
		m.mu.Unlock()
		return // already in a terminal state; don't clobber
	}
	conn.Status.Connected = false
	conn.Status.Starting = false
	if reason != nil {
		conn.Status.LastError = reason
	}
	for tn, t := range m.tools {
		if t.ServerName == name {
			delete(m.tools, tn)
		}
	}
	m.mu.Unlock()

	m.logger.Warn("MCP server disconnected",
		zap.String("server", name),
		zap.Error(reason))
}

// startServer starts a single MCP server via the configured transport.
func (m *Manager) startServer(ctx context.Context, conn *ServerConnection) error {
	switch conn.Config.Transport {
	case TransportStdio:
		return m.startStdioServer(ctx, conn)
	case TransportSSE:
		return m.startSSEServer(ctx, conn)
	default:
		return fmt.Errorf("unsupported transport: %s", conn.Config.Transport)
	}
}

// startStdioServer starts an MCP server communicating via stdin/stdout JSON-RPC 2.0.
func (m *Manager) startStdioServer(ctx context.Context, conn *ServerConnection) error {
	m.logger.Info("starting MCP stdio server",
		zap.String("server", conn.Config.Name),
		zap.String("command", conn.Config.Command))

	transport, err := newStdioTransport(ctx, conn.Config, m.logger)
	if err != nil {
		return fmt.Errorf("failed to start stdio transport: %w", err)
	}

	// Mark the server as disconnected the moment the child process or
	// pipe goes away — covers a server that crashes mid-session, a
	// `kill -9 npx`, or just stdin EPIPE on the next Call. Without
	// this, /mcp status would keep showing "connected" against a dead
	// process and only /mcp restart could recover.
	srvName := conn.Config.Name
	transport.onClose = func(reason error) {
		m.markDisconnected(srvName, reason)
	}

	conn.transport = transport
	conn.Process = transport.cmd.Process

	// Initialize the MCP protocol
	if err := m.initializeServer(conn); err != nil {
		_ = transport.Close()
		conn.transport = nil
		conn.Process = nil
		return fmt.Errorf("MCP initialize failed: %w", err)
	}

	// Discover tools
	if err := m.discoverTools(conn); err != nil {
		m.logger.Warn("MCP tool discovery failed (server may not support tools/list)",
			zap.String("server", conn.Config.Name),
			zap.Error(err))
	}

	conn.Status.Connected = true
	conn.Status.Starting = false
	conn.Status.LastError = nil
	conn.Status.StartedAt = time.Now()
	m.logger.Info("MCP stdio server connected",
		zap.String("server", conn.Config.Name),
		zap.Int("tools", conn.Status.ToolCount))

	return nil
}

// startSSEServer connects to an MCP server via Server-Sent Events.
func (m *Manager) startSSEServer(ctx context.Context, conn *ServerConnection) error {
	m.logger.Info("connecting to MCP SSE server",
		zap.String("server", conn.Config.Name),
		zap.String("url", conn.Config.URL))

	transport, err := newSSETransport(ctx, conn.Config, m.logger, m.channels, conn.Config.Name)
	if err != nil {
		return fmt.Errorf("failed to connect SSE transport: %w", err)
	}

	conn.transport = transport

	// Initialize the MCP protocol
	if err := m.initializeServer(conn); err != nil {
		_ = transport.Close()
		conn.transport = nil
		return fmt.Errorf("MCP initialize failed: %w", err)
	}

	// Discover tools
	if err := m.discoverTools(conn); err != nil {
		m.logger.Warn("MCP tool discovery failed",
			zap.String("server", conn.Config.Name),
			zap.Error(err))
	}

	conn.Status.Connected = true
	conn.Status.Starting = false
	conn.Status.LastError = nil
	conn.Status.StartedAt = time.Now()
	m.logger.Info("MCP SSE server connected",
		zap.String("server", conn.Config.Name),
		zap.Int("tools", conn.Status.ToolCount))

	return nil
}

// initializeServer sends the MCP initialize request.
func (m *Manager) initializeServer(conn *ServerConnection) error {
	params := initializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities:    capabilities{},
		ClientInfo: clientInfo{
			Name:    "chatcli",
			Version: "1.0.0",
		},
	}

	result, err := conn.transport.Call("initialize", params)
	if err != nil {
		return err
	}

	var initResult initializeResult
	if err := json.Unmarshal(result, &initResult); err != nil {
		m.logger.Debug("MCP initialize response parse warning", zap.Error(err))
	}

	m.logger.Info("MCP server initialized",
		zap.String("server", conn.Config.Name),
		zap.String("protocol", initResult.ProtocolVersion),
		zap.String("serverName", initResult.ServerInfo.Name))

	// Send initialized notification (no response expected)
	_, _ = conn.transport.Call("notifications/initialized", nil)

	return nil
}

// discoverTools calls tools/list on the MCP server and registers discovered tools.
func (m *Manager) discoverTools(conn *ServerConnection) error {
	result, err := conn.transport.Call("tools/list", nil)
	if err != nil {
		return err
	}

	var toolsList toolsListResult
	if err := json.Unmarshal(result, &toolsList); err != nil {
		return fmt.Errorf("parsing tools/list: %w", err)
	}

	m.mu.Lock()
	for _, t := range toolsList.Tools {
		m.tools[t.Name] = &MCPTool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
			ServerName:  conn.Config.Name,
		}
	}
	conn.Status.ToolCount = len(toolsList.Tools)
	m.mu.Unlock()

	return nil
}

// callTool sends a tools/call request to the MCP server via its transport.
func (m *Manager) callTool(ctx context.Context, conn *ServerConnection, toolName string, args map[string]interface{}) (*MCPToolResult, error) {
	if conn.transport == nil {
		return nil, fmt.Errorf("MCP server %q has no active transport", conn.Config.Name)
	}

	params := toolCallParams{
		Name:      toolName,
		Arguments: args,
	}

	result, err := conn.transport.Call("tools/call", params)
	if err != nil {
		return nil, err
	}

	var callResult toolCallResult
	if err := json.Unmarshal(result, &callResult); err != nil {
		return nil, fmt.Errorf("parsing tools/call result: %w", err)
	}

	// Combine text content
	var content string
	var mimeType string
	for _, c := range callResult.Content {
		switch c.Type {
		case "text":
			content += c.Text
		case "resource":
			content += c.Data
			mimeType = c.MimeType
		}
	}

	return &MCPToolResult{
		Content:  content,
		IsError:  callResult.IsError,
		MimeType: mimeType,
	}, nil
}
