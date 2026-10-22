/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package runs

import (
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/pkg/textfold"
)

// Entry is one line of a run's activity log: a timestamped record of a
// turn change, an action label, a free-form note or the final status. The
// log is the richest per-run record the runtime keeps — the LLM
// conversation of a run is never stored here — and it is what @agents
// transcript and /agents expose after a run has finished.
type Entry struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// DefaultActivityCap bounds the activity log kept per run; the oldest
// entries are dropped first so a long monitoring loop cannot grow memory.
const DefaultActivityCap = 200

// maxActivityTextBytes bounds one entry so a worker's final output excerpt
// stays a summary, not a transcript.
const maxActivityTextBytes = 2000

// appendActivityLocked records one entry under r.mu (caller holds it).
func (r *Run) appendActivityLocked(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len(text) > maxActivityTextBytes {
		text = textfold.Cut(text, maxActivityTextBytes) + "…"
	}
	r.log = append(r.log, Entry{At: time.Now(), Text: text})
	if overflow := len(r.log) - DefaultActivityCap; overflow > 0 {
		r.log = append(r.log[:0], r.log[overflow:]...)
	}
}

// Note appends a free-form line to the run's activity log (nil-safe). The
// dispatcher uses it to keep an excerpt of a worker's final output next to
// the run record so the orchestrator can review a finished run.
func (r *Run) Note(text string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.appendActivityLocked(text)
	r.mu.Unlock()
}

// activitySnapshotLocked returns a copy of the log (caller holds r.mu).
func (r *Run) activitySnapshotLocked() []Entry {
	if len(r.log) == 0 {
		return nil
	}
	out := make([]Entry, len(r.log))
	copy(out, r.log)
	return out
}

// ActivityLog returns a copy of one run's activity log — live or finished —
// oldest first. The second result is false when the ID is unknown; a known
// run with nothing recorded yet returns an empty log and true.
func (g *Registry) ActivityLog(id string) ([]Entry, bool) {
	if g == nil || id == "" {
		return nil, false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if run, ok := g.active[id]; ok {
		run.mu.Lock()
		defer run.mu.Unlock()
		return run.activitySnapshotLocked(), true
	}
	log, ok := g.doneLogs[id]
	if !ok {
		return nil, false
	}
	out := make([]Entry, len(log))
	copy(out, log)
	return out, true
}

// formatEnd renders the terminal entry of a run.
func formatEnd(info Info) string {
	line := fmt.Sprintf("end status=%s tool_calls=%d elapsed=%s", info.Status, info.ToolCalls, info.Elapsed().Round(time.Second))
	if info.Err != "" {
		line += " error=" + info.Err
	}
	return line
}
