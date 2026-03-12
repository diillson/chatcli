package tui

import (
	"context"

	"github.com/diillson/chatcli/models"
)

// Completion represents an autocomplete suggestion.
type Completion struct {
	Text        string
	Description string
}

// TokenUsage holds current token consumption info.
type TokenUsage struct {
	Used  int
	Limit int
	Cost  float64
}

// FileChange represents a modified file with diff stats.
type FileChange struct {
	Path      string
	Additions int
	Deletions int
}

// Task represents a step in a plan.
type Task struct {
	Description string
	Status      string // "pending", "running", "done", "error"
}

// TaskInfo is used by CLIBridge to report agent tasks without importing agent package.
type TaskInfo struct {
	Description string
	Status      string
}

// MCPServer represents an MCP server's status for display.
type MCPServer struct {
	Name      string
	Connected bool
	ToolCount int
}

// MCPServerInfo is used by CLIBridge to report MCP server status.
type MCPServerInfo struct {
	Name      string
	Connected bool
	ToolCount int
}

// ContextInfo represents an attached context for display.
type ContextInfo struct {
	Name      string
	FileCount int
	SizeBytes int64
}

// CheckpointInfo represents a conversation checkpoint for rewind.
type CheckpointInfo struct {
	Index    int
	Label    string
	Time     string
	MsgCount int
}

// Backend is the interface that decouples the TUI from ChatCLI internals.
// The TUI never knows about "agent mode" or "coder mode" — it just renders events.
type Backend interface {
	// SendMessage sends user input and returns a channel of events.
	// The backend decides whether to use tools, do multi-turn, etc.
	SendMessage(ctx context.Context, input string) (<-chan Event, error)

	// CancelOperation cancels the current LLM/tool operation.
	CancelOperation()

	// GetHistory returns the conversation history.
	GetHistory() []models.Message

	// Metadata
	GetModelName() string
	GetProvider() string
	GetSessionName() string
	GetWorkingDir() string

	// GetCompletions returns autocomplete suggestions for the given prefix.
	GetCompletions(prefix string) []Completion

	// Layout
	SetContentWidth(w int)

	// Sidebar data
	GetTokenUsage() TokenUsage
	GetModifiedFiles() []FileChange
	GetTasks() []Task
	GetMCPServers() []MCPServer
	GetAttachedContexts() []ContextInfo
}

// New creates the root Bubble Tea model wired to the given backend.
func New(backend Backend) Model {
	return newModel(backend)
}
