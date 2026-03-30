package hooks

import "time"

// EventType represents the type of lifecycle event.
type EventType string

const (
	// EventSessionStart fires when a new CLI session begins.
	EventSessionStart EventType = "SessionStart"
	// EventSessionEnd fires when a CLI session ends.
	EventSessionEnd EventType = "SessionEnd"
	// EventPreToolUse fires before a tool is executed.
	EventPreToolUse EventType = "PreToolUse"
	// EventPostToolUse fires after a tool is executed successfully.
	EventPostToolUse EventType = "PostToolUse"
	// EventPostToolUseFailure fires after a tool execution fails.
	EventPostToolUseFailure EventType = "PostToolUseFailure"
	// EventUserPromptSubmit fires when the user submits a prompt.
	EventUserPromptSubmit EventType = "UserPromptSubmit"
	// EventPreCompact fires before history compaction.
	EventPreCompact EventType = "PreCompact"
	// EventPostCompact fires after history compaction.
	EventPostCompact EventType = "PostCompact"
	// EventNotification fires for user-facing notifications.
	EventNotification EventType = "Notification"
)

// AllEventTypes lists all valid event types for configuration validation.
var AllEventTypes = []EventType{
	EventSessionStart,
	EventSessionEnd,
	EventPreToolUse,
	EventPostToolUse,
	EventPostToolUseFailure,
	EventUserPromptSubmit,
	EventPreCompact,
	EventPostCompact,
	EventNotification,
}

// HookType represents how the hook is executed.
type HookType string

const (
	HookTypeCommand HookType = "command" // Shell command
	HookTypeHTTP    HookType = "http"    // HTTP POST
)

// HookConfig defines a single hook in the configuration.
type HookConfig struct {
	// Name is a human-readable identifier for this hook.
	Name string `json:"name"`
	// Event is the lifecycle event that triggers this hook.
	Event EventType `json:"event"`
	// Type determines how the hook is executed (command or http).
	Type HookType `json:"type"`
	// Command is the shell command to run (for command hooks).
	Command string `json:"command,omitempty"`
	// URL is the endpoint to POST to (for http hooks).
	URL string `json:"url,omitempty"`
	// Timeout is the maximum execution time in milliseconds (default: 10000).
	Timeout int `json:"timeout,omitempty"`
	// Enabled controls whether this hook is active (default: true).
	Enabled *bool `json:"enabled,omitempty"`
	// ToolPattern filters PreToolUse/PostToolUse events by tool name glob (e.g., "mcp_*", "@coder").
	ToolPattern string `json:"toolPattern,omitempty"`
}

// IsEnabled returns true if the hook is enabled (default: true).
func (h HookConfig) IsEnabled() bool {
	if h.Enabled == nil {
		return true
	}
	return *h.Enabled
}

// GetTimeout returns the timeout in milliseconds (default: 10000).
func (h HookConfig) GetTimeout() int {
	if h.Timeout <= 0 {
		return 10000
	}
	return h.Timeout
}

// HookEvent is the payload passed to a hook when it fires.
type HookEvent struct {
	// Type is the event that triggered the hook.
	Type EventType `json:"type"`
	// Timestamp is when the event occurred.
	Timestamp time.Time `json:"timestamp"`
	// ToolName is the tool being used (for tool events).
	ToolName string `json:"toolName,omitempty"`
	// ToolArgs is the tool arguments (for tool events).
	ToolArgs string `json:"toolArgs,omitempty"`
	// ToolOutput is the tool output (for PostToolUse events).
	ToolOutput string `json:"toolOutput,omitempty"`
	// Error is the error message (for failure events).
	Error string `json:"error,omitempty"`
	// UserPrompt is the user's input (for UserPromptSubmit).
	UserPrompt string `json:"userPrompt,omitempty"`
	// SessionID is the current session identifier.
	SessionID string `json:"sessionId,omitempty"`
	// WorkingDir is the current working directory.
	WorkingDir string `json:"workingDir,omitempty"`
}

// HookResult is the result of a hook execution.
type HookResult struct {
	// ExitCode is the exit code of the command (0 = allow, 2 = block).
	ExitCode int `json:"exitCode"`
	// Output is the stdout of the hook command.
	Output string `json:"output,omitempty"`
	// Error is stderr or error message.
	Error string `json:"error,omitempty"`
	// Blocked indicates whether the hook blocked the action (exit code 2).
	Blocked bool `json:"blocked"`
	// BlockReason is the reason for blocking (from stderr when exit code 2).
	BlockReason string `json:"blockReason,omitempty"`
}

// HooksConfig is the top-level configuration for hooks in settings.
type HooksConfig struct {
	Hooks []HookConfig `json:"hooks"`
}
