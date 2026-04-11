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
	AgentTypeFile        AgentType = "file"
	AgentTypeCoder       AgentType = "coder"
	AgentTypeShell       AgentType = "shell"
	AgentTypeGit         AgentType = "git"
	AgentTypeSearch      AgentType = "search"
	AgentTypePlanner     AgentType = "planner"
	AgentTypeReviewer    AgentType = "reviewer"
	AgentTypeTester      AgentType = "tester"
	AgentTypeRefactor    AgentType = "refactor"
	AgentTypeDiagnostics AgentType = "diagnostics"
	AgentTypeFormatter   AgentType = "formatter"
	AgentTypeDeps        AgentType = "deps"
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
	CallID        string
	Agent         AgentType
	Task          string
	Output        string
	Error         error
	Duration      time.Duration
	ToolCalls     []ToolCallRecord
	ParallelCalls int // number of tool calls that ran in parallel (0 = sequential)
}

// ToolCallRecord logs a tool call executed by a worker.
type ToolCallRecord struct {
	Name   string
	Args   string
	Output string
	Error  error
}

// AgentEventType identifies the kind of progress event emitted by the dispatcher.
type AgentEventType int

const (
	// AgentEventStarted is emitted when an agent begins execution.
	AgentEventStarted AgentEventType = iota
	// AgentEventCompleted is emitted when an agent finishes successfully.
	AgentEventCompleted
	// AgentEventFailed is emitted when an agent finishes with an error.
	AgentEventFailed
)

// AgentEvent represents a real-time progress event from the dispatcher.
type AgentEvent struct {
	Type     AgentEventType
	CallID   string
	Agent    AgentType
	Task     string
	Duration time.Duration // only set for Completed/Failed
	Error    error         // only set for Failed
	Index    int           // 0-based index of this agent in the batch
	Total    int           // total agents in the batch
}

// WorkerAgent is the interface every specialized agent must implement.
//
// The Model() and Effort() methods declare per-worker LLM preferences.
// Empty strings mean "inherit the user's active provider/model and send no
// effort hint". The dispatcher runs both through the shared model resolver
// (llm/client.ResolveModelRouting) so cross-provider swap, graceful
// fallback, and extended thinking / reasoning_effort happen uniformly.
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
	// Model returns this agent's preferred model id ("" = inherit).
	Model() string
	// Effort returns this agent's default effort level: "low", "medium",
	// "high", "max", or "" (no hint).
	Effort() string
	// Execute runs the agent on a task with the provided dependencies.
	Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error)
}

// PolicyChecker abstracts security policy enforcement for worker tool calls.
// Implementations must be safe for concurrent use from multiple goroutines.
// When a policy action is "ask", the implementation serializes interactive
// prompts so only one worker blocks on stdin at a time.
type PolicyChecker interface {
	// CheckAndPrompt checks the policy for a tool call. If the policy requires
	// user confirmation ("ask"), it prompts the user interactively (serialized
	// across goroutines). Returns (true, "") if allowed, or (false, message)
	// if denied/blocked.
	CheckAndPrompt(ctx context.Context, toolName, args string) (allowed bool, message string)
}

// Context keys for passing agent metadata through context.Context.
type ctxKey string

const (
	// CtxKeyAgentName carries the agent type name (e.g., "shell", "coder").
	CtxKeyAgentName ctxKey = "agent_name"
	// CtxKeyAgentTask carries the natural language task description.
	CtxKeyAgentTask ctxKey = "agent_task"
)

// WorkerDeps holds dependencies injected into each worker at execution time.
type WorkerDeps struct {
	LLMClient     client.LLMClient
	LockMgr       *FileLockManager
	PolicyChecker PolicyChecker // nil = no policy enforcement (all commands allowed)
	Logger        *zap.Logger
}
