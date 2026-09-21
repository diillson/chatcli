/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pulse

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"time"
)

// Span is one timed occurrence on the bus: a tool call, an MCP request, an
// outbound connection. Begin emits the start, End emits the matching end
// with the elapsed time. A nil Span is valid and does nothing, which is what
// Begin returns while the bus is off — so a tap is two unconditional calls
// and costs one atomic load when nobody is watching.
type Span struct {
	bus   *Bus
	ev    Event
	start time.Time
	done  atomic.Bool
}

var spanSeq atomic.Uint64

// Begin opens a span on the process-wide bus. parent is the ID of the node
// the work hangs from (a run ID, or empty for the session).
func Begin(kind Kind, name, parent string) *Span {
	return Default().Begin(kind, name, parent)
}

// Begin opens a span on this bus.
func (b *Bus) Begin(kind Kind, name, parent string) *Span {
	if !b.Enabled() {
		return nil
	}
	s := &Span{
		bus:   b,
		start: time.Now(),
		ev: Event{
			Kind:   kind,
			ID:     string(kind) + "-" + strconv.FormatUint(spanSeq.Add(1), 10),
			Parent: parent,
			Name:   name,
		},
	}
	open := s.ev
	open.Phase, open.Status = PhaseStart, StatusRunning
	b.Emit(open)
	return s
}

// With attaches an attribute that travels on the end event. Empty values
// are skipped. It returns the span so calls chain.
func (s *Span) With(key, value string) *Span {
	if s == nil {
		return nil
	}
	s.ev = s.ev.With(key, value)
	return s
}

// End closes the span with the given status. Only the first End counts, so
// a deferred safety-net End after an explicit one is harmless.
func (s *Span) End(status string) {
	if s == nil || s.done.Swap(true) {
		return
	}
	closing := s.ev.Took(time.Since(s.start))
	closing.Phase, closing.Status = PhaseEnd, status
	s.bus.Emit(closing)
}

// EndErr closes the span as ok, cancelled or error according to err. The
// error text itself is never emitted.
func (s *Span) EndErr(err error) {
	s.End(StatusOf(err))
}

// StatusOf maps an error onto a bus status.
func StatusOf(err error) string {
	switch {
	case err == nil:
		return StatusOK
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return StatusCancelled
	default:
		return StatusError
	}
}

// Point emits a one-shot occurrence on the process-wide bus: something that
// happened and has no duration of its own (a skill activating, a tool call
// refused by policy).
func Point(kind Kind, name, parent, status string, attrs map[string]string) {
	b := Default()
	if !b.Enabled() {
		return
	}
	b.Emit(Event{
		Kind:   kind,
		Phase:  PhasePoint,
		ID:     string(kind) + "-" + strconv.FormatUint(spanSeq.Add(1), 10),
		Parent: parent,
		Name:   name,
		Status: status,
		Attrs:  attrs,
	})
}
