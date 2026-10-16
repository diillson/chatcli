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
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/cli/plugins"
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

// pulseToolContext returns ctx with the tool call's span as the innermost
// parent, so what the tool starts hangs from it.
func (a *AgentMode) pulseToolContext(ctx context.Context, callID string) context.Context {
	return a.pulseTools[callID].Context(ctx)
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

// pulseReactEnd closes the span of the orchestrator's ReAct loop (pattern
// #1) with how far it got. A park is the loop choosing to sleep until a
// wake, not a failure.
func (a *AgentMode) pulseReactEnd(span *pulse.Span, err error) {
	turns := strconv.Itoa(a.runTurns) + " turns"
	if errors.Is(err, errAgentParkedRequested) {
		span.Outcome(pulse.StatusOK, "parked after "+turns)
		return
	}
	span.Outcome(pulse.StatusOf(err), turns)
}

// tracedExecute runs a builtin outside the agent loop's dispatch — a chat
// turn's sanctioned tools, a MoA member's recall, a worker's context tools
// — as a tool node on the dashboard, hung from whatever started it. origin
// says which path ran it; arguments and output stay off the bus, only the
// subcommand and the output size travel.
func tracedExecute(ctx context.Context, p plugins.Plugin, argv []string, origin string) (string, error) {
	ctx, span := pulse.BeginCtx(ctx, pulse.KindTool, p.Name(), runs.FromContext(ctx).ID())
	if span == nil {
		return p.Execute(ctx, argv)
	}
	span.With("origin", origin).With("subcmd", toolSubcmd(argv))
	out, err := p.Execute(ctx, argv)
	span.With("out_bytes", strconv.Itoa(len(out))).EndErr(err)
	return out, err
}

// toolSubcmd is the subcommand of a call in envelope ({"cmd":...}) or argv
// form, "" when there is none. Only a short identifier counts: a builtin
// that takes a positional value first (@webfetch's URL, @websearch's query
// words) must never put that value on the bus.
func toolSubcmd(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	first := strings.TrimSpace(argv[0])
	if strings.HasPrefix(first, "{") {
		var env struct {
			Cmd string `json:"cmd"`
		}
		if json.Unmarshal([]byte(first), &env) != nil {
			return ""
		}
		first = env.Cmd
	}
	if !toolSubcmdRe.MatchString(first) {
		return ""
	}
	return first
}

// toolSubcmdRe is what a subcommand looks like: a short lower-case word.
var toolSubcmdRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,23}$`)
