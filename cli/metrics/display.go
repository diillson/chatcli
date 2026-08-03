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
	// Telemetry is a pre-formatted, locale-aware, " · "-joined telemetry
	// string (token in/out, context %, cost, compression savings) appended to
	// the end of the turn line. A plain string (not a slice) keeps TurnStats
	// comparable and the metrics package free of any cli/i18n/catalog import.
	Telemetry string
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

		// Live telemetry (tokens · ctx% · cost · savings), pre-formatted by
		// the caller. Mirrors what chat mode shows in its envelope footer.
		if stats.Telemetry != "" {
			p = append(p, fmt.Sprintf("%s%s%s", ColorGray, stats.Telemetry, ColorReset))
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
	// Live progress (fed from the agent run registry between redraw ticks).
	Turn     int    // current ReAct turn (0 = unknown)
	MaxTurns int    // turn budget (0 = unknown)
	Action   string // current action label, e.g. "read cli/foo.go"
	// SubLines holds pre-formatted, newline-separated lines rendered
	// indented under this agent (e.g. live subagents spawned by it). Kept
	// as a single string so AgentSlot stays comparable.
	SubLines string
}

// subLineList splits the newline-separated SubLines into displayable lines.
func (s AgentSlot) subLineList() []string {
	if s.SubLines == "" {
		return nil
	}
	return strings.Split(s.SubLines, "\n")
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

// SetLive refreshes a slot's live progress fields (current turn, action and
// subagent sub-lines). Fed from the agent run registry on each redraw tick;
// zero/empty values clear the corresponding field.
func (p *AgentProgressState) SetLive(callID string, turn, maxTurns int, action string, subLines []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Agents {
		if p.Agents[i].CallID == callID {
			p.Agents[i].Turn = turn
			p.Agents[i].MaxTurns = maxTurns
			p.Agents[i].Action = action
			p.Agents[i].SubLines = strings.Join(subLines, "\n")
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
			if slot.Turn > 0 && slot.MaxTurns > 0 {
				statusText = fmt.Sprintf("turno %d/%d", slot.Turn, slot.MaxTurns)
			}
			if slot.Action != "" {
				statusText += fmt.Sprintf(" · %s", truncateDisplay(slot.Action, 44))
			}
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
		for _, sub := range slot.subLineList() {
			fmt.Fprintf(&b, "      %s%s%s\n", ColorGray, truncateDisplay(sub, 90), ColorReset)
		}
	}

	return b.String()
}

// LineCount returns the number of display lines FormatDispatchProgress produces.
func (p *AgentProgressState) LineCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 1 + len(p.Agents) // header + one per agent
	for _, s := range p.Agents {
		n += len(s.subLineList())
	}
	return n
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
