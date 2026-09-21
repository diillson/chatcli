/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for chatcli acting as a server: the MCP server and the ACP
 * agent an IDE drives. Both speak JSON-RPC through one dispatch, so one tap
 * there covers every inbound request of both, and one in Request covers what
 * the server asks of the client (the permission dialogs).
 *
 * These surfaces have no prompt to type into and their stdout IS the
 * protocol stream, so nothing here ever prints: the dashboard in another
 * terminal is the only window into them. Only the method name is reported,
 * never the params or the result.
 */
package rpcserve

import (
	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseMethodMaxLen bounds a method name shown on the dashboard.
const pulseMethodMaxLen = 48

// pulseMethodName returns the method as a node name. The method comes off the
// wire, and each distinct name becomes a node: a client sending arbitrary
// names must not be able to flood the graph or smuggle text onto it, so
// anything that does not look like a protocol method collapses into one node.
func pulseMethodName(method string) string {
	if method == "" || len(method) > pulseMethodMaxLen {
		return "other"
	}
	for _, r := range method {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '/', r == '_', r == '.', r == '-', r == '$':
		default:
			return "other"
		}
	}
	return method
}

// pulseInbound opens the span of a request the client sent.
func pulseInbound(method string) *pulse.Span {
	if !pulse.Enabled() {
		return nil
	}
	return pulse.Begin(pulse.KindRPC, pulseMethodName(method), "").With("direction", "inbound")
}

// pulseOutbound opens the span of a request the server sends to the client
// and waits on, which is where a permission dialog nobody answers shows up as
// a call that never ends.
func pulseOutbound(method string) *pulse.Span {
	if !pulse.Enabled() {
		return nil
	}
	return pulse.Begin(pulse.KindRPC, "client:"+pulseMethodName(method), "").With("direction", "outbound")
}

// pulseEndRPC closes an inbound span from the handler's outcome.
func pulseEndRPC(span *pulse.Span, rpcErr *RPCError) {
	if rpcErr != nil {
		span.End(pulse.StatusError)
		return
	}
	span.End(pulse.StatusOK)
}
