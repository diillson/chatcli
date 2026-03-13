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
	servers map[string]*ServerConnection
	tools   map[string]*MCPTool // tool name -> tool
	mu      sync.RWMutex
	logger  *zap.Logger
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
		servers: make(map[string]*ServerConnection),
		tools:   make(map[string]*MCPTool),
		logger:  logger,
	}
}

// LoadConfig loads MCP server configurations from a JSON file.
func (m *Manager) LoadConfig(configPath string) error {
	data, err := os.ReadFile(configPath)
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
				Status: ServerStatus{Name: cfg.Name},
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
func (m *Manager) StartAll(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for name, conn := range m.servers {
		if err := m.startServer(ctx, conn); err != nil {
			m.logger.Warn("failed to start MCP server",
				zap.String("server", name),
				zap.Error(err))
		}
	}
	return nil
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

	transport, err := newSSETransport(ctx, conn.Config, m.logger)
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
