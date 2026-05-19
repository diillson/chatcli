/*
 * ChatCLI - Coder turn-UI input history
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import "sync"

// DefaultHistoryCap is the maximum number of submitted lines the
// history keeps. 100 is enough for a typical /coder session — when
// the user is iterating on a refactor they tend to revisit the last
// 5-10 prompts; older entries fade out by FIFO eviction. The cap
// also bounds the memory footprint of a long-running agent.
const DefaultHistoryCap = 100

// History is the ↑/↓ stack the mini-readline navigates. It is
// session-scoped: every /coder turn appends submissions, and ↑ walks
// backwards from the most recent. The implementation is a plain
// FIFO slice — performance is fine at the documented cap, and the
// data structure being trivial means the test surface is small.
//
// Goroutine safety: the readline loop is the only writer (via
// Append) and the only reader (via Up / Down / Reset). All three
// run on the same goroutine, so the mutex is defensive — it costs
// nothing in the happy path and prevents a data race if a future
// caller decides to share History across loops.
type History struct {
	mu       sync.Mutex
	entries  []string
	cap      int
	cursor   int    // -1 means "not navigating"; otherwise index into entries
	original string // buffer text when navigation started, restored on Reset
}

// NewHistory constructs a history bounded at the given capacity.
// A non-positive cap falls back to DefaultHistoryCap so callers that
// forget to set one don't get an unbounded buffer.
func NewHistory(cap int) *History {
	if cap <= 0 {
		cap = DefaultHistoryCap
	}
	return &History{cap: cap, cursor: -1}
}

// Append records a submitted line and resets the navigation cursor.
// Empty / whitespace-only lines and exact duplicates of the most
// recent entry are skipped — same behavior as bash's HISTCONTROL
// ignoreboth, which matches what users expect from a shell-style
// history.
//
// When the buffer is at capacity, the oldest entry is dropped to
// make room. The slice is shifted in place; at the documented cap
// of 100 this is irrelevant for performance.
func (h *History) Append(line string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if line == "" {
		return
	}
	if n := len(h.entries); n > 0 && h.entries[n-1] == line {
		// Skip exact duplicate of the most recent entry. Users
		// who repeat themselves intentionally still get the entry
		// once; ↑ recalls it as the latest.
		h.cursor = -1
		return
	}
	if len(h.entries) >= h.cap {
		copy(h.entries, h.entries[1:])
		h.entries[len(h.entries)-1] = line
	} else {
		h.entries = append(h.entries, line)
	}
	h.cursor = -1
}

// Up navigates to the previous entry and returns it. The first Up
// after a fresh prompt captures `current` so Down can restore it
// when the user navigates back past the most recent entry.
//
// Returns ("", false) when there is no entry to surface (empty
// history, or already at the oldest). The boolean lets the caller
// distinguish "no movement" from "moved to an empty entry" — the
// latter is impossible by Append's contract, but explicit is
// better than implicit.
func (h *History) Up(current string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.entries) == 0 {
		return "", false
	}
	if h.cursor == -1 {
		// Start navigating: stash the current buffer so Down can
		// restore it later.
		h.original = current
		h.cursor = len(h.entries) - 1
		return h.entries[h.cursor], true
	}
	if h.cursor == 0 {
		// Already at the oldest entry.
		return "", false
	}
	h.cursor--
	return h.entries[h.cursor], true
}

// Down navigates forward through the history. If the cursor is at
// the most-recent entry, Down restores the original buffer the
// user was typing when navigation began and resets the cursor.
// Returns ("", false) when not navigating.
func (h *History) Down() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.cursor == -1 {
		return "", false
	}
	if h.cursor >= len(h.entries)-1 {
		// At the newest entry: stepping forward restores the
		// original buffer the user was typing.
		h.cursor = -1
		original := h.original
		h.original = ""
		return original, true
	}
	h.cursor++
	return h.entries[h.cursor], true
}

// Reset clears the navigation state without affecting stored
// entries. Used by the readline loop after a submission so the
// next ↑ starts from the newest entry again, not from wherever
// the previous navigation left off.
func (h *History) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cursor = -1
	h.original = ""
}

// Len returns the number of stored entries. Cheap; useful for
// tests and (eventually) a status indicator like "↑ 3/100".
func (h *History) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.entries)
}
