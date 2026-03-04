package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

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
	Config  ServerConfig
	Status  ServerStatus
	Process *os.Process
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
		if conn.Process != nil {
			m.logger.Info("stopping MCP server", zap.String("server", name))
			_ = conn.Process.Kill()
			conn.Process = nil
			conn.Status.Connected = false
		}
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

// startServer starts a single MCP server via stdio transport.
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

// startStdioServer starts an MCP server communicating via stdin/stdout.
func (m *Manager) startStdioServer(ctx context.Context, conn *ServerConnection) error {
	// TODO: Implement JSON-RPC over stdio transport
	// This requires spawning the command and managing stdin/stdout pipes
	// with the MCP protocol (JSON-RPC 2.0)
	m.logger.Info("MCP stdio server registered (lazy start)",
		zap.String("server", conn.Config.Name),
		zap.String("command", conn.Config.Command))
	conn.Status.Connected = true
	return nil
}

// startSSEServer connects to an MCP server via Server-Sent Events.
func (m *Manager) startSSEServer(ctx context.Context, conn *ServerConnection) error {
	// TODO: Implement SSE transport
	m.logger.Info("MCP SSE server registered",
		zap.String("server", conn.Config.Name),
		zap.String("url", conn.Config.URL))
	conn.Status.Connected = true
	return nil
}

// callTool sends a tool execution request to the MCP server.
func (m *Manager) callTool(ctx context.Context, conn *ServerConnection, toolName string, args map[string]interface{}) (*MCPToolResult, error) {
	// TODO: Implement actual JSON-RPC call to MCP server
	// For now, return a placeholder indicating MCP is available but not fully wired
	return &MCPToolResult{
		Content: fmt.Sprintf("MCP tool %q called with args %v (transport implementation pending)", toolName, args),
		IsError: false,
	}, nil
}
