/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Who is asking. LogRequestStart is the one chokepoint every provider passes
 * through, but it receives no context, so a request could be logged, audited
 * and shown on the live dashboard without anyone being able to say which
 * agent run made it. With parallel workers on the same model that is the
 * difference between "the squad spent 40 requests" and "the reviewer did".
 *
 * The adapters pass CallerField(ctx) along with their other fields. What a
 * caller is stays outside this package: the CLI installs a resolver that
 * reads the agent run from the context, so llm keeps no dependency on it.
 * A second resolver names the innermost live-dashboard node the request
 * hangs from (a chat turn, a MoA panel, a tool call), carried in its own
 * field so the caller keeps meaning "the agent run" in the request log and
 * the audit trail.
 */
package client

import (
	"context"
	"sync/atomic"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// CallerFieldKey is the log and audit field carrying the caller id.
const CallerFieldKey = "caller"

// callerResolver holds a func(context.Context) string.
var callerResolver atomic.Value

// SetCallerResolver installs the function that names the caller of a request
// from its context (nil clears it). It returns "" when the request belongs to
// no one in particular: a chat turn, a background job.
func SetCallerResolver(fn func(context.Context) string) {
	if fn == nil {
		fn = func(context.Context) string { return "" }
	}
	callerResolver.Store(fn)
}

// ParentFieldKey is the field carrying the live-dashboard node a request
// hangs from, when it differs from the caller.
const ParentFieldKey = "pulse_parent"

// parentResolver holds a func(context.Context) string.
var parentResolver atomic.Value

// SetParentResolver installs the function that names the live-dashboard
// node a request made under ctx hangs from (nil clears it).
func SetParentResolver(fn func(context.Context) string) {
	if fn == nil {
		fn = func(context.Context) string { return "" }
	}
	parentResolver.Store(fn)
}

// CallerField is the field an adapter adds to LogRequestStart. It is a no-op
// field when no resolver is installed or the request has no caller, so the
// log line and the audit entry only grow when there is something to say.
// When the request hangs from a dashboard node other than its caller, the
// field carries that node too, under ParentFieldKey.
func CallerField(ctx context.Context) zap.Field {
	if ctx == nil {
		return zap.Skip()
	}
	var id, parent string
	if fn, _ := callerResolver.Load().(func(context.Context) string); fn != nil {
		id = fn(ctx)
	}
	if fn, _ := parentResolver.Load().(func(context.Context) string); fn != nil {
		if p := fn(ctx); p != id {
			parent = p
		}
	}
	switch {
	case parent != "":
		return zap.Inline(callerInfo{caller: id, parent: parent})
	case id != "":
		return zap.String(CallerFieldKey, id)
	}
	return zap.Skip()
}

// callerInfo is a request's caller and the dashboard node it hangs from.
type callerInfo struct{ caller, parent string }

func (c callerInfo) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	if c.caller != "" {
		enc.AddString(CallerFieldKey, c.caller)
	}
	enc.AddString(ParentFieldKey, c.parent)
	return nil
}
