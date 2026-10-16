/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for the web UI's API: each route is one node on the
 * dashboard, named by its template ("POST tools/{1}"), never by the raw path,
 * which carries tool and skill names, session ids and query strings. A turn
 * runs under its request's node, so the agent run and the LLM calls it starts
 * hang from the route that started it.
 */
package webui

import (
	"context"
	"net/http"
	"strconv"

	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseRouteName names a route by its template: the method, the resource and
// how many path segments follow it.
func pulseRouteName(method, resource string, segments int) string {
	name := "web:" + method + " " + resource
	for i := 1; i <= segments; i++ {
		name += "/{" + strconv.Itoa(i) + "}"
	}
	return name
}

// pulseBeginRoute opens the span of one API request and returns the context
// its handler must run under. Nothing about the request is reported beyond
// the route template and the method.
func pulseBeginRoute(ctx context.Context, method, name string) (context.Context, *pulse.Span) {
	if !pulse.Enabled() {
		return ctx, nil
	}
	ctx, span := pulse.BeginCtx(ctx, pulse.KindRPC, name, "")
	return ctx, span.With("direction", "inbound").With("method", method)
}

// pulseEndRoute closes a route span with the response status. A refusal the
// page expects (no auth, a turn already running, a rate limit) is blocked; any
// other 4xx or 5xx is an error.
func pulseEndRoute(span *pulse.Span, code int) {
	if span == nil {
		return
	}
	span = span.With("status", strconv.Itoa(code))
	switch {
	case code == http.StatusUnauthorized, code == http.StatusForbidden,
		code == http.StatusConflict, code == http.StatusTooManyRequests:
		span.End(pulse.StatusBlocked)
	case code >= http.StatusBadRequest:
		span.End(pulse.StatusError)
	default:
		span.End(pulse.StatusOK)
	}
}

// pulseStatusWriter records the status a handler writes. It forwards Flush,
// which the streamed turn needs, and unwraps for http.ResponseController.
type pulseStatusWriter struct {
	http.ResponseWriter
	code int
}

func (w *pulseStatusWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *pulseStatusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer when it streams.
func (w *pulseStatusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (w *pulseStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// status is the code the handler sent, 200 when it wrote nothing.
func (w *pulseStatusWriter) status() int {
	if w.code == 0 {
		return http.StatusOK
	}
	return w.code
}
