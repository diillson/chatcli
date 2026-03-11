package cli

import (
	"fmt"
	"strings"
	"time"
)

// OutputEmitter abstracts terminal output so that agent_mode.go can render
// to either a classic terminal (fmt.Print) or the Bubble Tea TUI viewport.
type OutputEmitter interface {
	// EmitText prints plain text (no trailing newline implied).
	EmitText(text string)

	// EmitLine prints text followed by a newline.
	EmitLine(text string)

	// EmitLinef prints formatted text followed by a newline.
	EmitLinef(format string, args ...interface{})

	// EmitTurnStart signals the beginning of an agent turn.
	EmitTurnStart(turn, maxTurns int)

	// EmitTurnEnd signals the end of an agent turn with timing info.
	EmitTurnEnd(turn, maxTurns int, duration time.Duration, toolCalls, agents int)

	// EmitToolStart signals a tool execution is beginning.
	EmitToolStart(toolName, description string)

	// EmitToolResult signals a tool execution completed.
	EmitToolResult(toolName string, exitCode int, output string, duration time.Duration)

	// EmitThinking signals the LLM is processing (for spinner/timer display).
	EmitThinking(model string, duration time.Duration)

	// EmitThinkingDone clears the thinking indicator.
	EmitThinkingDone()

	// EmitMarkdown renders a markdown block.
	EmitMarkdown(icon, title, content, color string)

	// EmitStatus prints a status/info message.
	EmitStatus(text string)

	// EmitError prints an error message.
	EmitError(text string)

	// ClearLine clears the current terminal line (no-op in TUI).
	ClearLine()

	// ClearLines clears N lines above cursor (no-op in TUI).
	ClearLines(n int)

	// RequestApproval asks the user to approve a dangerous command.
	// Returns true if approved, false if denied/skipped.
	// For terminal mode, this reads from stdin. For TUI mode, this sends
	// an EventNeedApproval and blocks until the user responds.
	RequestApproval(command, description, risk string) bool
}

// terminalEmitter is the default OutputEmitter that writes to stdout,
// preserving the existing terminal behavior.
type terminalEmitter struct{}

// NewTerminalEmitter returns the default terminal-based emitter.
func NewTerminalEmitter() OutputEmitter {
	return &terminalEmitter{}
}

func (e *terminalEmitter) EmitText(text string)  { fmt.Print(text) }
func (e *terminalEmitter) EmitLine(text string)   { fmt.Println(text) }
func (e *terminalEmitter) EmitLinef(format string, args ...interface{}) {
	fmt.Printf(format+"\n", args...)
}

func (e *terminalEmitter) EmitTurnStart(turn, maxTurns int) {
	// Terminal mode: no-op, the timer callback handles the display
}

func (e *terminalEmitter) EmitTurnEnd(turn, maxTurns int, duration time.Duration, toolCalls, agents int) {
	// Terminal mode: handled by showTurnStats in processAIResponseAndAct
}

func (e *terminalEmitter) EmitToolStart(toolName, description string) {
	fmt.Printf("\n\033[33m╭── 🔨 %s\033[0m\n", toolName)
	if description != "" {
		fmt.Printf("\033[33m│\033[0m  %s\n", description)
	}
}

func (e *terminalEmitter) EmitToolResult(toolName string, exitCode int, output string, duration time.Duration) {
	icon := "✅"
	color := "\033[32m"
	if exitCode != 0 {
		icon = "❌"
		color = "\033[31m"
	}
	if output != "" {
		display := output
		if len(display) > 2000 {
			display = display[:2000] + "\n... [truncated]"
		}
		lines := strings.Split(display, "\n")
		for _, line := range lines {
			fmt.Printf("\033[33m│\033[0m  %s\n", line)
		}
	}
	fmt.Printf("%s╰── %s %s (%s)\033[0m\n", color, icon, toolName, duration.Round(time.Millisecond))
}

func (e *terminalEmitter) EmitThinking(model string, duration time.Duration) {
	// Terminal mode: handled by turnTimer callback
}

func (e *terminalEmitter) EmitThinkingDone() {
	// Terminal mode: handled by turnTimer.Stop()
}

func (e *terminalEmitter) EmitMarkdown(icon, title, content, color string) {
	fmt.Printf("\n%s%s %s\033[0m\n", color, icon, title)
	if content != "" {
		for _, line := range strings.Split(content, "\n") {
			fmt.Printf("  %s\n", line)
		}
	}
}

func (e *terminalEmitter) EmitStatus(text string) {
	fmt.Println(text)
}

func (e *terminalEmitter) EmitError(text string) {
	fmt.Println(text)
}

func (e *terminalEmitter) ClearLine() {
	fmt.Print("\033[2K\r")
}

func (e *terminalEmitter) ClearLines(n int) {
	for i := 0; i < n; i++ {
		fmt.Print("\033[A\033[2K")
	}
}

func (e *terminalEmitter) RequestApproval(command, description, risk string) bool {
	// Terminal mode: the existing policyAdapter handles this via stdin
	// This is a fallback that always approves (actual policy check is elsewhere)
	return true
}
