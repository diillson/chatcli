/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * gateway_events_sink — structured progress for gateway turns.
 *
 * Instead of scraping the rendered stdout (ANSI stripping + line
 * heuristics), the gateway installs this agentevents.Sink for the run and
 * receives typed events at the points where semantics are known: reasoning
 * parsed, tool classified, result structured. Each event becomes one clean
 * chat-friendly progress line, batched by the runner's progressSink.
 *
 * The legacy stdout-scraping path remains behind
 * CHATCLI_GATEWAY_STRUCTURED_PROGRESS=false as a rollback switch.
 */
package cli

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/agentevents"
)

// gatewayEventsSink converts agent events into progress lines. Safe for
// concurrent use (plugin stream callbacks may fire from other goroutines).
type gatewayEventsSink struct {
	mu       sync.Mutex
	emit     func(string)
	lastLine string
}

// newGatewayEventsSink builds a sink over a progress emitter.
func newGatewayEventsSink(emit func(string)) *gatewayEventsSink {
	if emit == nil {
		emit = func(string) {}
	}
	return &gatewayEventsSink{emit: emit}
}

var _ agentevents.Sink = (*gatewayEventsSink)(nil)

// send emits a line, dropping empties and consecutive duplicates.
func (s *gatewayEventsSink) send(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	s.mu.Lock()
	if line == s.lastLine {
		s.mu.Unlock()
		return
	}
	s.lastLine = line
	emit := s.emit
	s.mu.Unlock()
	emit(line)
}

// oneLine flattens text to a single clipped line.
func oneLine(text string, maxLen int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > maxLen {
		return text[:maxLen] + "…"
	}
	return text
}

// Thought delivers reasoning text as a lightweight status line.
func (s *gatewayEventsSink) Thought(text string) {
	if line := oneLine(text, 160); line != "" {
		s.send("🧠 " + line)
	}
}

// Message is the assistant's prose — the final reply reaches the platform
// through the runner's "final" send, so progress skips it.
func (s *gatewayEventsSink) Message(string) {}

// ToolStart announces a tool call entering execution.
func (s *gatewayEventsSink) ToolStart(tc agentevents.ToolCall) {
	title := tc.Title
	if title == "" {
		title = tc.Name
	}
	s.send("▸ " + oneLine(title, 120))
}

// ToolEnd delivers the terminal state of a tool call.
func (s *gatewayEventsSink) ToolEnd(tc agentevents.ToolCall) {
	title := tc.Title
	if title == "" {
		title = tc.Name
	}
	icon := "✓"
	if tc.IsError {
		icon = "✗"
	}
	line := fmt.Sprintf("%s %s", icon, oneLine(title, 120))
	if tc.Duration > 0 {
		line += fmt.Sprintf(" (%s)", tc.Duration.Round(time.Millisecond))
	}
	s.send(line)
}

// PlanUpdate summarizes the task plan as a compact progress counter plus
// the step currently in flight.
func (s *gatewayEventsSink) PlanUpdate(p agentevents.Plan) {
	if len(p.Entries) == 0 {
		return
	}
	done := 0
	current := ""
	for _, e := range p.Entries {
		switch e.Status {
		case "completed":
			done++
		case "in_progress":
			if current == "" {
				current = e.Content
			}
		}
	}
	line := fmt.Sprintf("📋 %d/%d", done, len(p.Entries))
	if current != "" {
		line += " · " + oneLine(current, 100)
	}
	s.send(line)
}
