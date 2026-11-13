/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package client

import (
	"context"
	"net/http"
)

// A caller that shows progress wants to know that a reply is arriving, not
// only that it arrived. Usage counts come once, when a call ends; the text
// itself is the only thing that moves while a provider streams. A caller
// puts a sink in the context and every path that decodes streamed text
// reports the size of each piece to it: the chunk channel of the streaming
// clients (WatchStream) and the server-sent-event loops of the providers
// that stream behind the buffered SendPrompt. A call that is not streamed
// reports nothing, and a context without a sink costs one lookup.

type streamProgressKey struct{}

// StreamProgressRestart is the count a sink receives when a decoder starts
// reading a response: whatever an earlier attempt of the same call reported
// was thrown away with that attempt (a retry re-sends the request and the
// reply starts over), so the sink must start over too.
const StreamProgressRestart = -1

// WithStreamProgress returns a context whose streamed text is reported to
// fn, one call per piece with its length in bytes, and StreamProgressRestart
// each time an attempt begins. fn runs on the goroutine that decodes the
// stream and must not block.
func WithStreamProgress(ctx context.Context, fn func(chars int)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, streamProgressKey{}, fn)
}

// WithoutStreamProgress returns a context that reports to no sink, for an
// auxiliary request made on behalf of a call (a title, a keep-alive) whose
// text is not the reply the caller is following.
func WithoutStreamProgress(ctx context.Context) context.Context {
	return context.WithValue(ctx, streamProgressKey{}, (func(chars int))(nil))
}

// ReportStreamProgress tells the sink in ctx, if any, that chars bytes of
// reply text were just decoded.
func ReportStreamProgress(ctx context.Context, chars int) {
	if ctx == nil || chars <= 0 {
		return
	}
	if fn, ok := ctx.Value(streamProgressKey{}).(func(chars int)); ok && fn != nil {
		fn(chars)
	}
}

// RestartStreamProgress tells the sink in ctx, if any, that an attempt is
// starting over.
func RestartStreamProgress(ctx context.Context) {
	if ctx == nil {
		return
	}
	if fn, ok := ctx.Value(streamProgressKey{}).(func(chars int)); ok && fn != nil {
		fn(StreamProgressRestart)
	}
}

// RestartResponseProgress tells the sink of the request resp answers that
// a decoder is starting on a new response.
func RestartResponseProgress(resp *http.Response) {
	if resp == nil || resp.Request == nil {
		return
	}
	if fn, ok := resp.Request.Context().Value(streamProgressKey{}).(func(chars int)); ok && fn != nil {
		fn(StreamProgressRestart)
	}
}

// ReportResponseProgress is ReportStreamProgress for a decoder that holds
// the HTTP response rather than the context it was requested under: the
// sink is read from the request the response answers.
func ReportResponseProgress(resp *http.Response, chars int) {
	if resp == nil || resp.Request == nil || chars <= 0 {
		return
	}
	if fn, ok := resp.Request.Context().Value(streamProgressKey{}).(func(chars int)); ok && fn != nil {
		fn(chars)
	}
}
