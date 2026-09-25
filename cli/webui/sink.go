/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/agentevents"
)

// event is one server-sent event on a run's stream. Type names the kind;
// the other fields are filled per kind and omitted otherwise.
type event struct {
	Type      string      `json:"type"`
	Run       string      `json:"run,omitempty"`
	Text      string      `json:"text,omitempty"`
	Tool      *toolEvent  `json:"tool,omitempty"`
	Plan      []planEntry `json:"plan,omitempty"`
	ID        string      `json:"id,omitempty"`
	Reason    string      `json:"reason,omitempty"`
	OfferAll  bool        `json:"offer_always,omitempty"`
	Reply     string      `json:"reply,omitempty"`
	Error     string      `json:"error,omitempty"`
	Cancelled bool        `json:"cancelled,omitempty"`
}

type toolEvent struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Title    string `json:"title,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Status   string `json:"status,omitempty"`
	Input    string `json:"input,omitempty"`
	Output   string `json:"output,omitempty"`
	IsError  bool   `json:"is_error,omitempty"`
	Duration int64  `json:"duration_ms,omitempty"`
}

type planEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status,omitempty"`
}

// toolOutputCap bounds one tool output on the wire; the full output reached
// the model, the page shows a summary and can expand nothing more.
const toolOutputCap = 16 * 1024

// runSink turns the engine's structured events into stream events and
// answers permission requests from the browser. It is safe for concurrent
// use: plugin callbacks may fire off the loop goroutine.
type runSink struct {
	runID   string
	ctx     context.Context
	events  chan event
	timeout time.Duration

	mu      sync.Mutex
	seq     int
	pending map[string]chan agentevents.PermissionDecision
	closed  bool
}

var _ agentevents.Sink = (*runSink)(nil)
var _ agentevents.PermissionDecider = (*runSink)(nil)

func newRunSink(ctx context.Context, runID string, timeout time.Duration) *runSink {
	return &runSink{runID: runID, ctx: ctx, events: make(chan event, 256), timeout: timeout, pending: map[string]chan agentevents.PermissionDecision{}}
}

// push queues an event; a page that stopped reading does not block the
// engine, the event is dropped once the buffer is full. The run context is
// deliberately not consulted here: the final event of a cancelled run must
// still reach the page, and a ready Done channel would race it away.
func (s *runSink) push(ev event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	ev.Run = s.runID
	select {
	case s.events <- ev:
	default:
	}
}

// Chunk implements cli.ChunkSink: streamed chat text.
func (s *runSink) Chunk(text string) { s.push(event{Type: "chunk", Text: text}) }

func (s *runSink) Thought(text string) { s.push(event{Type: "thought", Text: text}) }
func (s *runSink) Message(text string) { s.push(event{Type: "message", Text: text}) }

func (s *runSink) ToolStart(tc agentevents.ToolCall) {
	s.push(event{Type: "tool_start", Tool: toolEventFrom(tc)})
}

func (s *runSink) ToolEnd(tc agentevents.ToolCall) {
	s.push(event{Type: "tool_end", Tool: toolEventFrom(tc)})
}

func (s *runSink) PlanUpdate(p agentevents.Plan) {
	entries := make([]planEntry, 0, len(p.Entries))
	for _, e := range p.Entries {
		entries = append(entries, planEntry{Content: e.Content, Priority: e.Priority, Status: e.Status})
	}
	s.push(event{Type: "plan", Plan: entries})
}

func toolEventFrom(tc agentevents.ToolCall) *toolEvent {
	out := tc.Output
	if tc.OmitContent {
		out = ""
	} else if len(out) > toolOutputCap {
		out = out[:toolOutputCap] + "\n…"
	}
	return &toolEvent{
		ID: tc.ID, Name: tc.Name, Title: tc.Title, Kind: string(tc.Kind), Status: string(tc.Status),
		Input: tc.RawInput, Output: out, IsError: tc.IsError, Duration: tc.Duration.Milliseconds(),
	}
}

// RequestPermissionDecision asks the page and waits for its answer. No
// answer within the timeout, or a run that ended, denies the action once:
// the loop then tells the model the action was refused instead of stalling.
func (s *runSink) RequestPermissionDecision(req agentevents.PermissionRequest) (agentevents.PermissionDecision, error) {
	s.mu.Lock()
	s.seq++
	id := strconv.Itoa(s.seq)
	ch := make(chan agentevents.PermissionDecision, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	s.push(event{Type: "permission", ID: id, Tool: toolEventFrom(req.Tool), Reason: req.Reason, OfferAll: req.OfferAlways})

	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	select {
	case d := <-ch:
		return d, nil
	case <-timer.C:
		return agentevents.PermissionDenyOnce, nil
	case <-s.ctx.Done():
		return agentevents.PermissionDenyOnce, nil
	}
}

// resolve delivers the browser's decision; false when the id is unknown or
// already answered.
func (s *runSink) resolve(id string, d agentevents.PermissionDecision) bool {
	s.mu.Lock()
	ch, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !ok {
		return false
	}
	ch <- d
	return true
}

// close stops accepting events; the stream reader drains what is queued.
func (s *runSink) close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.events)
	}
	s.mu.Unlock()
}
