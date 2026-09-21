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
 */
package client

import (
	"context"
	"sync/atomic"

	"go.uber.org/zap"
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

// CallerField is the field an adapter adds to LogRequestStart. It is a no-op
// field when no resolver is installed or the request has no caller, so the
// log line and the audit entry only grow when there is something to say.
func CallerField(ctx context.Context) zap.Field {
	fn, _ := callerResolver.Load().(func(context.Context) string)
	if fn == nil || ctx == nil {
		return zap.Skip()
	}
	if id := fn(ctx); id != "" {
		return zap.String(CallerFieldKey, id)
	}
	return zap.Skip()
}
