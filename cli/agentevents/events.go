/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Package agentevents defines the structured event surface the agent/coder
 * ReAct loop exposes to protocol frontends (ACP today; gateway/MCP later).
 *
 * The loop renders its terminal UI unconditionally; when a Sink is installed
 * for the run it ALSO emits typed events at the points where semantics are
 * known (reasoning parsed, tool classified, result structured). Frontends
 * consume the events instead of scraping the rendered stdout, so terminal
 * presentation and wire protocol evolve independently.
 *
 * This package is a leaf: stdlib only, importable from cli, cli/rpcserve and
 * cmd without cycles.
 */
package agentevents

import (
	"errors"
	"time"
)

// ErrPermissionUnsupported reports that the connected client does not
// implement the permission round-trip (e.g. an ACP client answering
// "method not found" to session/request_permission). Callers treat it as
// "no dialog exists" — the unattended fallback policy applies — which is
// different from a denial or a transport failure (both fail-safe deny).
var ErrPermissionUnsupported = errors.New("client does not support permission requests")

// ErrPermissionTimeout reports that the permission round-trip was sent but no
// user response arrived within the configured bound — typically a client that
// declared the capability without actually rendering a dialog. Callers deny
// fail-safe (the user may have wanted to say no) but should tell the model
// the request went UNANSWERED, not that the user denied it.
var ErrPermissionTimeout = errors.New("permission request timed out without a user response")

// ToolKind classifies what a tool call does, using the ACP spec vocabulary so
// protocol sinks can forward it verbatim.
type ToolKind string

const (
	KindRead    ToolKind = "read"
	KindEdit    ToolKind = "edit"
	KindDelete  ToolKind = "delete"
	KindMove    ToolKind = "move"
	KindSearch  ToolKind = "search"
	KindExecute ToolKind = "execute"
	KindThink   ToolKind = "think"
	KindFetch   ToolKind = "fetch"
	KindOther   ToolKind = "other"
)

// ToolStatus is the lifecycle state of a tool call (ACP spec vocabulary).
type ToolStatus string

const (
	StatusPending    ToolStatus = "pending"
	StatusInProgress ToolStatus = "in_progress"
	StatusCompleted  ToolStatus = "completed"
	StatusFailed     ToolStatus = "failed"
)

// Normalize clamps a kind to the ACP vocabulary: emitters may synthesize
// values, and an out-of-vocabulary kind on the wire is a schema violation.
func (k ToolKind) Normalize() ToolKind {
	switch k {
	case KindRead, KindEdit, KindDelete, KindMove, KindSearch, KindExecute, KindThink, KindFetch, KindOther:
		return k
	}
	return KindOther
}

// Normalize clamps a status to the ACP vocabulary. The zero value maps to
// pending — the spec's own default for unreported status.
func (s ToolStatus) Normalize() ToolStatus {
	switch s {
	case StatusPending, StatusInProgress, StatusCompleted, StatusFailed:
		return s
	}
	return StatusPending
}

// Terminal reports whether the status is a final state — sinks use it to
// decide between announcing a call and closing one.
func (s ToolStatus) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed
}

// Location points at a file (and optionally a line) a tool call touches, so
// IDE clients can offer "follow along" navigation.
type Location struct {
	Path string
	Line int
}

// ToolCall describes one tool invocation. The same value shape is used for
// the start event (Status=in_progress, Output empty) and the end event
// (Status=completed/failed, Output filled).
type ToolCall struct {
	ID        string
	Name      string // plugin name, e.g. "@coder"
	Title     string // human label (plugin DescribeCall), e.g. "Reading: main.go"
	Kind      ToolKind
	Status    ToolStatus
	RawInput  string // normalized args string as the model issued them
	Output    string // final output (post loop-side truncation); empty on start
	IsError   bool
	ErrorCode string
	Duration  time.Duration
	Locations []Location
	// OmitContent marks calls whose Output should not be shown as chat
	// content (e.g. whole-file reads): sinks surface title+locations only.
	OmitContent bool
}

// PlanEntry is one step of the agent's task plan. Status uses the ACP plan
// vocabulary: pending | in_progress | completed.
type PlanEntry struct {
	Content  string
	Priority string // high | medium | low
	Status   string
}

// Plan is a full snapshot of the agent's current task plan; sinks replace any
// previous plan with it.
type Plan struct {
	Entries []PlanEntry
}

// Sink receives structured events from one agent/coder run. Implementations
// must be safe for concurrent use: plugin stream callbacks may fire from
// other goroutines than the loop's.
type Sink interface {
	// Thought delivers reasoning/explanation text (not part of the answer).
	Thought(text string)
	// Message delivers assistant prose; the last Message of a run is the
	// final answer.
	Message(text string)
	// ToolStart announces a tool call entering execution.
	ToolStart(tc ToolCall)
	// ToolEnd delivers the terminal state of a tool call. Sinks must accept
	// IDs they never saw in ToolStart (calls blocked before execution).
	ToolEnd(tc ToolCall)
	// PlanUpdate replaces the current task plan snapshot.
	PlanUpdate(p Plan)
}

// PermissionRequester is an optional Sink capability: when implemented, the
// loop asks it to approve dangerous actions instead of applying the blanket
// unattended policy. The call blocks until the user decides; implementations
// must deny on timeout/cancel (fail-safe).
type PermissionRequester interface {
	RequestPermission(tc ToolCall, reason string) (bool, error)
}

// PermissionDecision is the outcome of a permission dialog, mirroring the
// terminal security prompt's vocabulary so protocol clients reach full
// parity with the interactive flow: the "always" variants carry the user's
// intent to persist the choice as a policy rule.
type PermissionDecision string

const (
	PermissionAllowOnce   PermissionDecision = "allow_once"
	PermissionAllowAlways PermissionDecision = "allow_always"
	PermissionDenyOnce    PermissionDecision = "deny_once"
	PermissionDenyAlways  PermissionDecision = "deny_always"
)

// Allowed reports whether the decision permits running the action.
func (d PermissionDecision) Allowed() bool {
	return d == PermissionAllowOnce || d == PermissionAllowAlways
}

// Persistent reports whether the decision should be recorded as a policy
// rule so future matches never reach a dialog.
func (d PermissionDecision) Persistent() bool {
	return d == PermissionAllowAlways || d == PermissionDenyAlways
}

// PermissionRequest describes one permission round-trip. A struct (rather
// than positional arguments) so the surface can grow without breaking
// implementations.
type PermissionRequest struct {
	// Tool is the gated call, already announced to the client when possible
	// so the dialog can anchor to the same tool-call id.
	Tool ToolCall
	// Reason is the human-readable explanation shown alongside the dialog.
	Reason string
	// OfferAlways adds the persistent allow/deny choices to the dialog.
	// Callers set it only when a policy pattern exists to persist the
	// decision against — exec commands, by design, never get one.
	OfferAlways bool
}

// PermissionDecider is the four-way refinement of PermissionRequester.
// Implementations that support the full terminal vocabulary (allow/deny,
// once/always) implement BOTH interfaces; the loop prefers this one and
// falls back to the boolean surface. Error contracts are identical:
// ErrPermissionUnsupported means "no dialog exists" (legacy fallback
// applies); any other error denies fail-safe.
type PermissionDecider interface {
	RequestPermissionDecision(req PermissionRequest) (PermissionDecision, error)
}
