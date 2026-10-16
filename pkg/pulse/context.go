/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pulse

import (
	"context"
	"strconv"
	"strings"
)

// The innermost node a piece of work hangs from travels in its context:
// whoever opens a span or a run puts its ID there, and whatever starts
// under that context — an LLM request, a tool call, a nested run — names it
// as its parent. That is what turns the dashboard from a list of hubs
// under the session into a map of who called what.

type parentKey struct{}

// WithParent returns ctx carrying id as the innermost parent. An empty id
// leaves ctx as it is.
func WithParent(ctx context.Context, id string) context.Context {
	if id == "" || ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, parentKey{}, id)
}

// ParentFrom returns the innermost parent carried by ctx, or "".
func ParentFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(parentKey{}).(string)
	return id
}

// ID returns the span's node ID, "" for a nil span.
func (s *Span) ID() string {
	if s == nil {
		return ""
	}
	return s.ev.ID
}

// Context returns ctx with this span as the innermost parent, so work
// started under it hangs from it. A nil span returns ctx unchanged.
func (s *Span) Context(ctx context.Context) context.Context {
	return WithParent(ctx, s.ID())
}

// BeginCtx opens a span parented to the innermost parent in ctx (fallback
// when there is none) and returns the context its own children should use.
func BeginCtx(ctx context.Context, kind Kind, name, fallback string) (context.Context, *Span) {
	parent := ParentFrom(ctx)
	if parent == "" {
		parent = fallback
	}
	s := Begin(kind, name, parent)
	return s.Context(ctx), s
}

// Update emits an update for a running span: attributes that changed while
// it runs (a round number, a running count). It does not end the span.
func (s *Span) Update(attrs map[string]string) {
	if s == nil || s.done.Load() {
		return
	}
	ev := s.ev
	ev.Phase, ev.Status = PhaseUpdate, StatusRunning
	ev.Attrs = nil
	for k, v := range attrs {
		ev = ev.With(k, v)
	}
	s.bus.Emit(ev)
}

// KindOf returns the kind of a span or point ID ("turn-12" → KindTurn),
// "" for anything else (a run ID, the session).
func KindOf(id string) Kind {
	i := strings.LastIndexByte(id, '-')
	if i <= 0 {
		return ""
	}
	if _, err := strconv.ParseUint(id[i+1:], 10, 64); err != nil {
		return ""
	}
	switch k := Kind(id[:i]); k {
	case KindTurn, KindLLM, KindTool, KindSkill, KindMCP, KindPattern, KindBackground, KindConn, KindRPC:
		return k
	}
	return ""
}
