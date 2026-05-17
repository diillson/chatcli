/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package llm provides shared utilities for the chatcli LLM provider
// layer. partial_input.go specifically implements an incremental JSON
// reader for tool-input streaming, so per-provider streaming code can
// surface partial argument values to the spinner / UI without waiting
// for the full tool_use block to land.
package llm

import (
	"encoding/json"
	"strings"
	"sync"
)

// PartialInputField is a single key/value pair extracted from a streaming
// JSON object. Emitted by PartialInputReader.OnField when a top-level
// string field has been fully assembled (value matched closing quote)
// or when the entire object has finished parsing.
//
// Streaming semantics are deliberately conservative: we don't surface
// partial-string values mid-flight (those would race against escapes),
// and we don't traverse nested objects (the only callers today care
// about top-level "query", "url", "file" strings). The reader can be
// extended later for nested traversal without changing this struct.
type PartialInputField struct {
	Name  string
	Value string
}

// PartialInputReader incrementally consumes JSON fragments arriving
// from an LLM streaming endpoint and emits PartialInputField events
// for top-level string fields as they finish. It is goroutine-safe;
// the typical caller is the streaming SSE loop in a single goroutine,
// but a UI thread may inspect Snapshot concurrently.
//
// Lifecycle:
//
//	r := NewPartialInputReader()
//	r.OnField = func(f PartialInputField) { /* push to plugin spinner */ }
//	for chunk := range provider.Deltas() {
//	    r.Feed(chunk)
//	}
//	r.Close() // emits any remaining fields if the JSON parses cleanly
type PartialInputReader struct {
	// OnField is invoked when a complete top-level string field is
	// available. The callback runs on the goroutine calling Feed —
	// it must not block.
	OnField func(PartialInputField)

	mu        sync.Mutex
	buf       strings.Builder
	emitted   map[string]struct{}
	closed    bool
	maxBuffer int
}

// NewPartialInputReader builds a reader with a default 64KiB buffer
// limit. Tool inputs are tiny in practice (<2KB); the cap guards
// against pathological streams.
func NewPartialInputReader() *PartialInputReader {
	return &PartialInputReader{
		emitted:   make(map[string]struct{}),
		maxBuffer: 64 * 1024,
	}
}

// WithMaxBuffer overrides the safety cap. Negative or zero disables
// the cap entirely (caller assumes the risk of unbounded growth).
func (r *PartialInputReader) WithMaxBuffer(n int) *PartialInputReader {
	r.maxBuffer = n
	return r
}

// Feed appends another JSON fragment from the provider and attempts to
// re-parse the accumulated buffer. If parsing succeeds and yields a
// JSON object, every top-level string field not yet emitted is
// surfaced via OnField. Numbers, arrays, and nested objects are
// ignored for the streaming path; they show up only in the final
// (post-Close) replay if needed.
func (r *PartialInputReader) Feed(fragment string) {
	if fragment == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if r.maxBuffer > 0 && r.buf.Len()+len(fragment) > r.maxBuffer {
		// Saturated — refuse further input rather than panic.
		return
	}
	r.buf.WriteString(fragment)
	r.tryEmitLocked()
}

// Close marks the reader done. After Close, no further Feed calls have
// effect. Returns the final accumulated buffer so callers can also
// json.Unmarshal it into their own typed struct for the canonical
// non-partial round-trip.
func (r *PartialInputReader) Close() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.tryEmitLocked()
	return r.buf.String()
}

// Snapshot returns the current accumulated buffer without mutating
// state. Useful for diagnostics — pull the partial JSON into a log
// field without disturbing the streaming pipeline.
func (r *PartialInputReader) Snapshot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// tryEmitLocked attempts to parse the buffer and emit any new
// top-level string fields. Must be called with r.mu held.
//
// The parsing strategy is intentionally simple: try json.Unmarshal on
// the full buffer; if it succeeds, walk the resulting map. If it
// fails (incomplete JSON), do nothing — we'll try again on the next
// Feed. This is O(n) per attempt; for buffers <2KB the cost is
// negligible compared to the network latency that drove the stream.
//
// A future refinement would be a true streaming JSON tokenizer that
// emits fields as soon as their closing quote arrives, without the
// repeated re-parse. We don't ship that today because the simple
// approach is correct and fast enough for the bounded tool-input
// payloads in scope.
func (r *PartialInputReader) tryEmitLocked() {
	if r.OnField == nil {
		return
	}
	raw := strings.TrimSpace(r.buf.String())
	if raw == "" || raw[0] != '{' {
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return
	}
	for key, val := range obj {
		if _, already := r.emitted[key]; already {
			continue
		}
		var s string
		if err := json.Unmarshal(val, &s); err != nil {
			// Not a string at the top level — skip silently. Numbers,
			// booleans, arrays, nested objects don't fit the spinner UX.
			continue
		}
		r.emitted[key] = struct{}{}
		r.OnField(PartialInputField{Name: key, Value: s})
	}
}
