package tui

import (
	"time"

	"github.com/diillson/chatcli/llm/client"
)

// EventType classifies events from the Backend.
type EventType int

const (
	EventTextDelta    EventType = iota // partial token from LLM (streaming)
	EventToolStart                     // tool call started
	EventToolResult                    // tool call finished
	EventNeedApproval                  // dangerous command, needs approval
	EventThinking                      // LLM reasoning block
	EventTurnStart                     // start of a ReAct turn
	EventPlanUpdate                    // task/plan update
	EventDone                          // response complete
	EventError                         // error
	EventExit                          // exit the application
	EventClear                         // clear viewport messages
	EventRewind                        // remove last user+assistant pair
)

// Event is the unified unit of communication from backend to TUI.
type Event struct {
	Type     EventType
	Text     string             // for TextDelta, CommandOutput
	Tool     *ToolEvent         // for ToolStart, ToolResult
	Approval *ApprovalRequest   // for NeedApproval
	Turn     *TurnInfo          // for TurnStart
	Tasks    []Task             // for PlanUpdate
	Usage    *client.UsageInfo  // for Done
	Error    error              // for Error
}

// ToolEvent carries tool call information.
type ToolEvent struct {
	Name     string
	Args     string
	Output   string
	ExitCode int
	Duration time.Duration
	Status   string // "running", "done", "error"
}

// ApprovalRequest is sent when a dangerous command needs user confirmation.
type ApprovalRequest struct {
	Command     string
	Description string
	Risk        string // "high", "medium"
	ResponseCh  chan<- ApprovalResponse
}

// ApprovalResponse is the user's answer to an approval request.
type ApprovalResponse int

const (
	ApprovalYes ApprovalResponse = iota
	ApprovalNo
	ApprovalAlways
	ApprovalSkip
)

// TurnInfo carries ReAct turn information.
type TurnInfo struct {
	Turn     int
	MaxTurns int
}

// --- tea.Msg types used inside the TUI ---

// BackendEventMsg wraps an Event for the Bubble Tea update loop.
type BackendEventMsg struct{ Event Event }

// StreamDoneMsg signals the event channel was closed.
type StreamDoneMsg struct{}
