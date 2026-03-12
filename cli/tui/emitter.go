package tui

import (
	"fmt"
	"time"
)

// TUIEmitter implements cli.OutputEmitter and sends events to a TUI event channel.
// It is used when agent_mode runs inside the Bubble Tea TUI.
type TUIEmitter struct {
	ch    chan<- Event
	width int // content width for markdown rendering
}

// NewTUIEmitter creates an emitter that forwards output to the given event channel.
func NewTUIEmitter(ch chan<- Event, width int) *TUIEmitter {
	if width <= 0 {
		width = 80
	}
	return &TUIEmitter{ch: ch, width: width}
}

func (e *TUIEmitter) EmitText(text string) {
	e.ch <- Event{Type: EventPreStyledText, Text: text}
}

func (e *TUIEmitter) EmitLine(text string) {
	e.ch <- Event{Type: EventPreStyledText, Text: text}
}

func (e *TUIEmitter) EmitLinef(format string, args ...interface{}) {
	e.ch <- Event{Type: EventPreStyledText, Text: fmt.Sprintf(format, args...)}
}

func (e *TUIEmitter) EmitTurnStart(turn, maxTurns int) {
	e.ch <- Event{
		Type: EventTurnStart,
		Turn: &TurnInfo{Turn: turn, MaxTurns: maxTurns},
	}
}

func (e *TUIEmitter) EmitTurnEnd(turn, maxTurns int, duration time.Duration, toolCalls, agents int) {
	e.ch <- Event{
		Type: EventPreStyledText,
		Text: fmt.Sprintf("[Turn %d/%d completed in %s — %d tool calls, %d agents]",
			turn, maxTurns, duration.Round(time.Millisecond), toolCalls, agents),
	}
}

func (e *TUIEmitter) EmitToolStart(toolName, description string) {
	e.ch <- Event{
		Type: EventToolStart,
		Tool: &ToolEvent{Name: toolName},
	}
}

func (e *TUIEmitter) EmitToolResult(toolName string, exitCode int, output string, duration time.Duration) {
	e.ch <- Event{
		Type: EventToolResult,
		Tool: &ToolEvent{
			Name:     toolName,
			ExitCode: exitCode,
			Output:   output,
			Duration: duration,
		},
	}
}

func (e *TUIEmitter) EmitThinking(model string, duration time.Duration) {
	e.ch <- Event{Type: EventStatusUpdate, Text: fmt.Sprintf("Thinking... (%s, %s)", model, duration.Round(time.Second))}
}

func (e *TUIEmitter) EmitThinkingDone() {
	// No-op in TUI — the spinner handles this via state transition
}

func (e *TUIEmitter) EmitMarkdown(icon, title, content, color string) {
	rendered := RenderMarkdown(content, e.width)
	e.ch <- Event{Type: EventPreStyledText, Text: fmt.Sprintf("\n%s %s\n%s\n", icon, title, rendered)}
}

func (e *TUIEmitter) EmitStatus(text string) {
	e.ch <- Event{Type: EventStatusUpdate, Text: text}
}

func (e *TUIEmitter) EmitError(text string) {
	e.ch <- Event{Type: EventError, Error: fmt.Errorf("%s", text)}
}

func (e *TUIEmitter) ClearLine() {
	// No-op in TUI — viewport doesn't need cursor manipulation
}

func (e *TUIEmitter) ClearLines(n int) {
	// No-op in TUI — viewport doesn't need cursor manipulation
}

func (e *TUIEmitter) RequestApproval(command, description, risk string) bool {
	// Create a response channel and send the approval request to the TUI
	responseCh := make(chan ApprovalResponse, 1)
	e.ch <- Event{
		Type: EventNeedApproval,
		Approval: &ApprovalRequest{
			Command:     command,
			Description: description,
			Risk:        risk,
			ResponseCh:  responseCh,
		},
	}

	// Block until the user responds via the TUI overlay
	resp := <-responseCh
	return resp == ApprovalYes || resp == ApprovalAlways
}
