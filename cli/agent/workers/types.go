package workers

import (
	"context"
	"time"

	"github.com/diillson/chatcli/llm/client"
	"go.uber.org/zap"
)

// AgentType identifies a specialized agent kind.
type AgentType string

const (
	AgentTypeFile    AgentType = "file"
	AgentTypeCoder   AgentType = "coder"
	AgentTypeShell   AgentType = "shell"
	AgentTypeGit     AgentType = "git"
	AgentTypeSearch  AgentType = "search"
	AgentTypePlanner AgentType = "planner"
)

// AgentCall represents a parsed <agent_call> directive from the orchestrator.
type AgentCall struct {
	Agent AgentType // which specialized agent to invoke
	Task  string    // natural language task description
	ID    string    // unique call ID for tracking
	Raw   string    // raw XML for logging/debugging
}

// AgentResult is the output of a single agent execution.
type AgentResult struct {
	CallID         string
	Agent          AgentType
	Task           string
	Output         string
	Error          error
	Duration       time.Duration
	ToolCalls      []ToolCallRecord
	ParallelCalls  int // number of tool calls that ran in parallel (0 = sequential)
}

// ToolCallRecord logs a tool call executed by a worker.
type ToolCallRecord struct {
	Name   string
	Args   string
	Output string
	Error  error
}

// WorkerAgent is the interface every specialized agent must implement.
type WorkerAgent interface {
	// Type returns the agent's type identifier.
	Type() AgentType
	// Name returns a human-readable name.
	Name() string
	// Description returns the agent's capability summary (for orchestrator catalog).
	Description() string
	// SystemPrompt returns the specialized system prompt for this agent.
	SystemPrompt() string
	// Skills returns the skill set available to this agent.
	Skills() *SkillSet
	// AllowedCommands returns the @coder subcommands this agent is permitted to use.
	AllowedCommands() []string
	// IsReadOnly returns true if this agent never writes files.
	IsReadOnly() bool
	// Execute runs the agent on a task with the provided dependencies.
	Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error)
}

// WorkerDeps holds dependencies injected into each worker at execution time.
type WorkerDeps struct {
	LLMClient client.LLMClient
	LockMgr   *FileLockManager
	Logger    *zap.Logger
}
