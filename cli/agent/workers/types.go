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
	// Phase 5/6 quality agents — registered like any other worker so
	// the orchestrator can also call them explicitly via <agent_call>.
	AgentTypeRefiner  AgentType = "refiner"
	AgentTypeVerifier AgentType = "verifier"
)

// AgentCall represents a parsed <agent_call> directive from the orchestrator.
type AgentCall struct {
	Agent AgentType // which specialized agent to invoke
	Task  string    // natural language task description
	ID    string    // unique call ID for tracking
	Raw   string    // raw XML for logging/debugging
}

// AgentResult is the output of a single agent execution.
//
// Metadata is the catch-all bag used by quality hooks to communicate
// across the pipeline (e.g. Verifier sets "verified_with_discrepancy"
// so Reflexion can react). Always lazy-allocated by SetMetadata so
// pre-quality call sites stay zero-cost.
type AgentResult struct {
	CallID        string
	Agent         AgentType
	Task          string
	Output        string
	Error         error
	Duration      time.Duration
	ToolCalls     []ToolCallRecord
	ParallelCalls int               // number of tool calls that ran in parallel (0 = sequential)
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// SetMetadata stores a key on the result, allocating Metadata if nil.
// Empty key is a no-op so callers don't need to nil-check the value.
func (r *AgentResult) SetMetadata(key, value string) {
	if r == nil || key == "" {
		return
	}
	if r.Metadata == nil {
		r.Metadata = make(map[string]string)
	}
	r.Metadata[key] = value
}

// MetadataFlag returns true when key is present and equals "true".
// Convenience for the common boolean-flag pattern.
func (r *AgentResult) MetadataFlag(key string) bool {
	if r == nil || r.Metadata == nil {
		return false
	}
	return r.Metadata[key] == "true"
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

// ExecutionPipeline wraps a worker.Execute call with optional pre/post
// processing (Self-Refine, CoVe, Reflexion, etc.). The dispatcher invokes
// it in place of agent.Execute when set; when nil, behavior is identical
// to a direct call. The interface lives in the workers package so the
// dispatcher can hold it without importing the quality package (which
// would create an import cycle: quality already imports workers).
type ExecutionPipeline interface {
	Run(ctx context.Context, agent WorkerAgent, task string, deps *WorkerDeps) (*AgentResult, error)
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
