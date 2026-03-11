// Go Multi-Agent - Metrics Display
/*
 * ChatCLI - CLI metrics
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package metrics

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	ColorReset  = "\033[0m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorRed    = "\033[31m"
	ColorCyan   = "\033[36m"
	ColorGray   = "\033[90m"
	ColorBold   = "\033[1m"
)

func FormatDurationShort(d time.Duration) string { return d.Round(time.Second).String() }
func FormatDuration(d time.Duration) string      { return d.Round(time.Second).String() }

func FormatTimerStatus(d time.Duration, model, msg string) string {
	spinner := GetSpinnerFrame()
	dots := GetDotsAnimation()
	return fmt.Sprintf("\r%s%s%s [%s%s%s%s] %s[%s]%s %s|%s %s%s%s%s", ColorCyan, spinner, ColorReset, ColorBold, ColorCyan, model, ColorReset, ColorGray, FormatDurationShort(d), ColorReset, ColorGray, ColorReset, ColorGray, msg, dots, ColorReset)
}
func FormatTimerComplete(d time.Duration) string {
	return fmt.Sprintf("%s%s %s", ColorGray, FormatDuration(d), ColorReset)
}

// TurnStats holds per-turn and accumulated session counters.
type TurnStats struct {
	// Per-turn counters (reset each turn)
	TurnAgents    int
	TurnToolCalls int
	// Session totals (accumulated across all turns)
	SessionAgents    int
	SessionToolCalls int
}

func FormatTurnInfo(t, m int, d time.Duration, stats *TurnStats) string {
	p := []string{fmt.Sprintf("%sTurn %d/%d%s", ColorCyan, t, m, ColorReset)}
	if d > 0 {
		p = append(p, FormatTimerComplete(d))
	}
	if stats != nil {
		var turnParts []string
		if stats.TurnAgents > 0 {
			label := "agent"
			if stats.TurnAgents > 1 {
				label = "agents"
			}
			turnParts = append(turnParts, fmt.Sprintf("%d %s", stats.TurnAgents, label))
		}
		if stats.TurnToolCalls > 0 {
			label := "tool call"
			if stats.TurnToolCalls > 1 {
				label = "tool calls"
			}
			turnParts = append(turnParts, fmt.Sprintf("%d %s", stats.TurnToolCalls, label))
		}
		if len(turnParts) > 0 {
			p = append(p, fmt.Sprintf("%s[%s]%s", ColorGray, strings.Join(turnParts, ", "), ColorReset))
		}

		// Show session totals when they differ from the turn (i.e., not the first turn with activity)
		if stats.SessionAgents > stats.TurnAgents || stats.SessionToolCalls > stats.TurnToolCalls {
			var sessParts []string
			if stats.SessionAgents > 0 {
				sessParts = append(sessParts, fmt.Sprintf("%d agents", stats.SessionAgents))
			}
			if stats.SessionToolCalls > 0 {
				sessParts = append(sessParts, fmt.Sprintf("%d tool calls", stats.SessionToolCalls))
			}
			if len(sessParts) > 0 {
				p = append(p, fmt.Sprintf("%s(session: %s)%s", ColorGray, strings.Join(sessParts, ", "), ColorReset))
			}
		}
	}
	return strings.Join(p, " ")
}

func ClearLine() string { return "\r\033[K" }

// ClearLines moves cursor up N lines and clears each one.
func ClearLines(n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("\033[A\033[K") // move up + clear line
	}
	return b.String()
}

// AgentProgressState tracks the live status of each agent in a dispatch batch.
// It is safe for concurrent use.
type AgentProgressState struct {
	mu        sync.Mutex
	Total     int
	Agents    []AgentSlot
	StartTime time.Time
}

// AgentSlot represents the current status of a single agent.
type AgentSlot struct {
	CallID   string
	Agent    string
	Task     string
	Status   AgentSlotStatus
	Duration time.Duration
	Error    string
}

// AgentSlotStatus represents the lifecycle state of an agent slot.
type AgentSlotStatus int

const (
	SlotPending AgentSlotStatus = iota
	SlotRunning
	SlotCompleted
	SlotFailed
)

// NewAgentProgressState creates a progress tracker for N agents.
func NewAgentProgressState(total int, agents []struct{ CallID, Agent, Task string }) *AgentProgressState {
	slots := make([]AgentSlot, total)
	for i, a := range agents {
		slots[i] = AgentSlot{
			CallID: a.CallID,
			Agent:  a.Agent,
			Task:   a.Task,
			Status: SlotPending,
		}
	}
	return &AgentProgressState{
		Total:     total,
		Agents:    slots,
		StartTime: time.Now(),
	}
}

// MarkStarted marks an agent as running.
func (p *AgentProgressState) MarkStarted(callID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Agents {
		if p.Agents[i].CallID == callID {
			p.Agents[i].Status = SlotRunning
			return
		}
	}
}

// MarkCompleted marks an agent as completed.
func (p *AgentProgressState) MarkCompleted(callID string, d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Agents {
		if p.Agents[i].CallID == callID {
			p.Agents[i].Status = SlotCompleted
			p.Agents[i].Duration = d
			return
		}
	}
}

// MarkFailed marks an agent as failed.
func (p *AgentProgressState) MarkFailed(callID string, d time.Duration, errMsg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Agents {
		if p.Agents[i].CallID == callID {
			p.Agents[i].Status = SlotFailed
			p.Agents[i].Duration = d
			p.Agents[i].Error = errMsg
			return
		}
	}
}

// completedCountLocked returns how many agents have finished (must hold mu).
func (p *AgentProgressState) completedCountLocked() int {
	n := 0
	for _, s := range p.Agents {
		if s.Status == SlotCompleted || s.Status == SlotFailed {
			n++
		}
	}
	return n
}

// CompletedCount returns how many agents have finished (completed + failed).
func (p *AgentProgressState) CompletedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.completedCountLocked()
}

// FormatDispatchProgress renders a multi-line live progress display.
func FormatDispatchProgress(state *AgentProgressState, model string) string {
	state.mu.Lock()
	defer state.mu.Unlock()

	var b strings.Builder

	elapsed := time.Since(state.StartTime)
	completed := state.completedCountLocked()
	pct := 0
	if state.Total > 0 {
		pct = (completed * 100) / state.Total
	}

	// Header line with spinner, model, elapsed, progress bar
	spinner := GetSpinnerFrame()
	bar := renderProgressBar(pct, 20)
	fmt.Fprintf(&b, "\r%s%s%s [%s%s%s%s] %s[%s]%s %s %s%d/%d agents%s %s(%d%%)%s",
		ColorCyan, spinner, ColorReset,
		ColorBold, ColorCyan, model, ColorReset,
		ColorGray, FormatDurationShort(elapsed), ColorReset,
		bar,
		ColorCyan, completed, state.Total, ColorReset,
		ColorGray, pct, ColorReset,
	)
	b.WriteString("\n")

	// Per-agent status lines
	for _, slot := range state.Agents {
		var icon, statusText, color string
		taskPreview := truncateDisplay(slot.Task, 50)

		switch slot.Status {
		case SlotPending:
			icon = "○"
			statusText = "pendente"
			color = ColorGray
		case SlotRunning:
			icon = GetSpinnerFrame()
			statusText = "executando..."
			color = ColorCyan
		case SlotCompleted:
			icon = "✓"
			statusText = fmt.Sprintf("concluido (%s)", slot.Duration.Round(time.Millisecond))
			color = ColorGreen
		case SlotFailed:
			icon = "✗"
			errPreview := truncateDisplay(slot.Error, 40)
			statusText = fmt.Sprintf("falhou (%s) %s", slot.Duration.Round(time.Millisecond), errPreview)
			color = ColorRed
		}

		fmt.Fprintf(&b, "  %s%s%s %s[%s]%s %s ─ %s%s%s\n",
			color, icon, ColorReset,
			ColorBold, slot.Agent, ColorReset,
			taskPreview,
			color, statusText, ColorReset,
		)
	}

	return b.String()
}

// LineCount returns the number of display lines FormatDispatchProgress produces.
func (p *AgentProgressState) LineCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return 1 + len(p.Agents) // header + one per agent
}

// renderProgressBar draws a simple ASCII progress bar: [████░░░░░░]
func renderProgressBar(pct, width int) string {
	if pct > 100 {
		pct = 100
	}
	filled := (pct * width) / 100
	empty := width - filled
	return fmt.Sprintf("%s[%s%s%s%s]%s",
		ColorGray,
		ColorGreen, strings.Repeat("█", filled),
		ColorGray, strings.Repeat("░", empty),
		ColorReset,
	)
}

// truncateDisplay truncates a string for display purposes.
func truncateDisplay(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
