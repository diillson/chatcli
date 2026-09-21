/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry taps for tool execution in the orchestrator loop. They ride
 * on emitToolStart / emitToolEnd, the pair that brackets every branch of the
 * loop's tool dispatch (plugins, MCP tools, delegate, @ask), so one tap
 * covers them all. Tool arguments and output never reach the bus: a tool
 * node carries its name, its kind, its outcome and its duration.
 */
package cli

import (
	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseToolBegin opens the telemetry span of a tool call. The loop runs on a
// single goroutine, so the open-span table needs no lock.
func (a *AgentMode) pulseToolBegin(tc agentevents.ToolCall) {
	if !pulse.Enabled() {
		return
	}
	span := pulse.Begin(pulse.KindTool, tc.Name, a.orchRun.ID()).With("tool_kind", string(tc.Kind))
	if span == nil {
		return
	}
	if a.pulseTools == nil {
		a.pulseTools = make(map[string]*pulse.Span)
	}
	a.pulseTools[tc.ID] = span
}

// pulseToolEnd closes the span of a finished tool call.
func (a *AgentMode) pulseToolEnd(callID string, execErr error) {
	span, ok := a.pulseTools[callID]
	if !ok {
		return
	}
	delete(a.pulseTools, callID)
	span.EndErr(execErr)
}

// pulseToolBlocked reports a tool call refused before it ever started.
func (a *AgentMode) pulseToolBlocked(toolName string) {
	if !pulse.Enabled() {
		return
	}
	pulse.Point(pulse.KindTool, toolName, a.orchRun.ID(), pulse.StatusBlocked, nil)
}

// pulseCloseOpenTools ends the spans the loop left open. A park and a
// cancelled turn leave the dispatch without reaching emitToolEnd; without
// this sweep those tools would read as running forever on the dashboard.
func (a *AgentMode) pulseCloseOpenTools() {
	for id, span := range a.pulseTools {
		span.End(pulse.StatusCancelled)
		delete(a.pulseTools, id)
	}
}
