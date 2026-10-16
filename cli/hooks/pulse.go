/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for lifecycle hooks. Every hook run is one background node
 * named after the event that fired it ("hook:PreToolUse"), hung from
 * whatever was running when it fired. It carries how the hook ran and how it
 * ended — never its command, its URL, the event payload or its output.
 */
package hooks

import (
	"context"
	"net/http"
	"strconv"

	"github.com/diillson/chatcli/pkg/pulse"
)

// hookTrace collects what a hook executor learns that its HookResult does
// not say: whether the deadline cut it off and, for HTTP, the status code.
type hookTrace struct {
	timedOut   bool
	httpStatus int
}

// hookHTTPClient is the client HTTP hooks post with: the process default
// transport, resolved per request exactly as http.DefaultClient would, with
// its connection metered on the dashboard. No client-level timeout — the
// per-hook deadline on the request context bounds it, as before.
func hookHTTPClient() *http.Client {
	return &http.Client{Transport: pulse.MeterTransport(http.DefaultTransport)}
}

// pulseHookBegin opens the node of one hook run.
func pulseHookBegin(ctx context.Context, hook HookConfig, event HookEvent) *pulse.Span {
	if !pulse.Enabled() {
		return nil
	}
	_, span := pulse.BeginCtx(ctx, pulse.KindBackground, "hook:"+string(event.Type), "")
	return span.With("type", string(hook.Type)).With("tool", pulseToolName(event.ToolName))
}

// pulseHookEnd closes the node with how the hook ended. A hook that blocked
// the action ends ok with blocked_action=true: blocking is the hook doing
// its job, and the refused tool already reports itself as blocked.
func pulseHookEnd(span *pulse.Span, hook HookConfig, result *HookResult, tr hookTrace) {
	if span == nil || result == nil {
		return
	}
	span = span.With("timeout", strconv.FormatBool(tr.timedOut)).With("blocked_action", strconv.FormatBool(result.Blocked))
	if hook.Type == HookTypeHTTP {
		if tr.httpStatus > 0 {
			span = span.With("http_status", strconv.Itoa(tr.httpStatus/100)+"xx")
		}
	} else {
		span = span.With("exit_code", strconv.Itoa(result.ExitCode))
	}
	switch {
	case tr.timedOut:
		span.End(pulse.StatusCancelled)
	case result.Blocked:
		span.End(pulse.StatusOK)
	case result.ExitCode != 0:
		span.End(pulse.StatusError)
	default:
		span.End(pulse.StatusOK)
	}
}

// pulseToolNameRunes bounds the tool name carried on a hook node.
const pulseToolNameRunes = 48

// pulseToolName bounds a tool name.
func pulseToolName(s string) string {
	if r := []rune(s); len(r) > pulseToolNameRunes {
		return string(r[:pulseToolNameRunes-1]) + "…"
	}
	return s
}
