/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package pulse is the process-wide live telemetry bus: every subsystem
// reports what it is doing as small, content-free events, and the live
// dashboard renders them as a graph of nodes (sessions, turns, LLM requests,
// tools, skills, agents, MCP servers, harness patterns, background work,
// outbound connections) joined by parent links.
//
// The package is a stdlib-only leaf so any layer can emit without an import
// cycle. Three rules hold everywhere:
//
//   - Off means free. With the bus disabled Emit is one atomic load.
//   - Emitting never blocks and never fails the caller. A full queue drops
//     the event and counts the drop.
//   - Events carry metadata only: names, sizes, durations, statuses, token
//     counts. Never prompt text, tool arguments or tool output.
package pulse

import "time"

// Kind names the type of node an event describes.
type Kind string

// Node kinds. The dashboard lays the graph out in lanes by kind.
const (
	KindSession    Kind = "session"
	KindTurn       Kind = "turn"
	KindLLM        Kind = "llm"
	KindTool       Kind = "tool"
	KindSkill      Kind = "skill"
	KindAgent      Kind = "agent"
	KindMCP        Kind = "mcp"
	KindPattern    Kind = "pattern"
	KindBackground Kind = "background"
	KindConn       Kind = "conn"
	KindRPC        Kind = "rpc"
)

// Phase places an event in the life of its node.
type Phase string

// Phases. A node opens with PhaseStart, may report PhaseUpdate any number of
// times and closes with PhaseEnd. PhasePoint is a one-shot occurrence that
// has no duration of its own (a skill activating, a cache miss).
const (
	PhaseStart  Phase = "start"
	PhaseUpdate Phase = "update"
	PhaseEnd    Phase = "end"
	PhasePoint  Phase = "point"
)

// Statuses shared by every kind, so the dashboard colors nodes uniformly.
const (
	StatusRunning   = "running"
	StatusOK        = "ok"
	StatusError     = "error"
	StatusCancelled = "cancelled"
	StatusBlocked   = "blocked"
)

// Event is one observation. ID identifies the node within its instance;
// Parent is the ID of the node it hangs from (empty for a root). Seq, TS and
// Instance are stamped by the bus — emitters leave them zero.
type Event struct {
	Seq      uint64            `json:"seq"`
	TS       time.Time         `json:"ts"`
	Instance string            `json:"instance,omitempty"`
	Kind     Kind              `json:"kind"`
	Phase    Phase             `json:"phase"`
	ID       string            `json:"id"`
	Parent   string            `json:"parent,omitempty"`
	Name     string            `json:"name,omitempty"`
	Status   string            `json:"status,omitempty"`
	DurMS    int64             `json:"dur_ms,omitempty"`
	Attrs    map[string]string `json:"attrs,omitempty"`
}

// With returns a copy of the event carrying one more attribute. Empty values
// are skipped so call sites can pass optional fields unconditionally.
func (e Event) With(key, value string) Event {
	if key == "" || value == "" {
		return e
	}
	attrs := make(map[string]string, len(e.Attrs)+1)
	for k, v := range e.Attrs {
		attrs[k] = v
	}
	attrs[key] = value
	e.Attrs = attrs
	return e
}

// Took returns a copy of the event carrying the elapsed time, in
// milliseconds. Sub-millisecond work reports 1 so it still reads as timed.
func (e Event) Took(d time.Duration) Event {
	if d <= 0 {
		return e
	}
	ms := d.Milliseconds()
	if ms == 0 {
		ms = 1
	}
	e.DurMS = ms
	return e
}
