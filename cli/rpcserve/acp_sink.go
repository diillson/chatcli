/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * acp_sink.go
 *
 * acpSink translates the agent loop's structured events (agentevents.Sink)
 * into ACP session/update notifications: agent_thought_chunk, tool_call,
 * tool_call_update, plan and agent_message_chunk. It also implements
 * agentevents.PermissionRequester on top of the server→client
 * session/request_permission request, so dangerous actions surface the
 * client's native approval dialog.
 */
package rpcserve

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/i18n"
)

// acpToolContentCap bounds the content of one tool_call_update. Full outputs
// already reach the model; the client view is a summary, not an archive.
const acpToolContentCap = 8 * 1024

// acpSink forwards one run's structured events to an ACP session. Safe for
// concurrent use (plugin stream callbacks may fire off-loop); the underlying
// Server.write serializes frames on the wire.
type acpSink struct {
	acp       *ACP
	sessionID string
	runCtx    context.Context

	// permTimeout bounds each session/request_permission round-trip; 0 waits
	// until runCtx dies (legacy). Shares the CHATCLI_MCP_PERMISSION_TIMEOUT
	// knob with the MCP elicitation bridge — like CHATCLI_MCP_TOOLS, the env
	// governs both RPC surfaces.
	permTimeout time.Duration

	mu         sync.Mutex
	open       map[string]bool // tool_call ids announced and not yet closed
	sawMessage bool
	// permDead flips after a permission timeout: the client demonstrably does
	// not answer the dialog, so later requests in this run fail fast instead
	// of stalling the loop for another timeout per gated action.
	permDead bool
}

func newACPSink(a *ACP, sessionID string, runCtx context.Context) *acpSink {
	return &acpSink{acp: a, sessionID: sessionID, runCtx: runCtx, open: map[string]bool{},
		permTimeout: mcpPermissionTimeout()}
}

var _ agentevents.Sink = (*acpSink)(nil)
var _ agentevents.PermissionRequester = (*acpSink)(nil)
var _ agentevents.PermissionDecider = (*acpSink)(nil)

func (s *acpSink) update(update map[string]interface{}) {
	if s.acp.notify == nil {
		return
	}
	_ = s.acp.notify("session/update", map[string]interface{}{
		"sessionId": s.sessionID,
		"update":    update,
	})
}

// Thought → agent_thought_chunk.
func (s *acpSink) Thought(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	s.update(map[string]interface{}{
		"sessionUpdate": "agent_thought_chunk",
		"content":       map[string]interface{}{"type": "text", "text": text},
	})
}

// Message → agent_message_chunk. Marks that the client received prose so the
// prompt handler can skip its final-reply fallback.
func (s *acpSink) Message(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	s.mu.Lock()
	s.sawMessage = true
	s.mu.Unlock()
	s.update(map[string]interface{}{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]interface{}{"type": "text", "text": text},
	})
}

// ToolStart → tool_call (status in_progress).
func (s *acpSink) ToolStart(tc agentevents.ToolCall) {
	s.mu.Lock()
	s.open[tc.ID] = true
	s.mu.Unlock()
	s.update(toolCallPayload(tc, true))
}

// ToolEnd → tool_call_update; ids never announced (calls blocked before
// execution) are emitted as a complete tool_call instead, per the ACP rule
// that updates may only reference known ids.
func (s *acpSink) ToolEnd(tc agentevents.ToolCall) {
	s.mu.Lock()
	known := s.open[tc.ID]
	delete(s.open, tc.ID)
	s.mu.Unlock()

	payload := toolCallPayload(tc, !known)
	s.update(payload)
}

// PlanUpdate → plan.
func (s *acpSink) PlanUpdate(p agentevents.Plan) {
	entries := make([]map[string]interface{}, 0, len(p.Entries))
	for _, e := range p.Entries {
		entries = append(entries, map[string]interface{}{
			"content":  e.Content,
			"priority": e.Priority,
			"status":   e.Status,
		})
	}
	s.update(map[string]interface{}{
		"sessionUpdate": "plan",
		"entries":       entries,
	})
}

// acpPermissionOptions maps the wire optionId of each dialog choice to its
// typed decision. IDs and kinds follow the ACP option vocabulary.
var acpPermissionOptions = map[string]agentevents.PermissionDecision{
	"allow-once":    agentevents.PermissionAllowOnce,
	"allow-always":  agentevents.PermissionAllowAlways,
	"reject-once":   agentevents.PermissionDenyOnce,
	"reject-always": agentevents.PermissionDenyAlways,
}

// RequestPermission preserves the boolean PermissionRequester surface for
// callers that cannot persist a decision: a two-choice dialog whose only
// affirmative outcome is allow-once.
func (s *acpSink) RequestPermission(tc agentevents.ToolCall, reason string) (bool, error) {
	d, err := s.RequestPermissionDecision(agentevents.PermissionRequest{Tool: tc, Reason: reason})
	return d.Allowed(), err
}

// RequestPermissionDecision → session/request_permission (server→client
// request). The dialog mirrors the terminal security prompt: allow/reject
// once always, plus the persistent variants when the caller can record the
// choice as a policy rule (OfferAlways). Anything but an explicit selection
// — cancel, transport error, unknown option, missing requester — denies
// (fail-safe).
func (s *acpSink) RequestPermissionDecision(req agentevents.PermissionRequest) (agentevents.PermissionDecision, error) {
	if s.acp.request == nil {
		return agentevents.PermissionDenyOnce, nil
	}
	tc := req.Tool
	title := tc.Title
	if req.Reason != "" {
		title = strings.TrimSpace(title + " — " + req.Reason)
	}
	toolCall := toolCallPayload(tc, true)
	delete(toolCall, "sessionUpdate")
	toolCall["title"] = title
	toolCall["status"] = "pending"

	options := []map[string]interface{}{
		{"optionId": "allow-once", "name": i18n.T("acp.permission.allow_once"), "kind": "allow_once"},
	}
	if req.OfferAlways {
		options = append(options,
			map[string]interface{}{"optionId": "allow-always", "name": i18n.T("acp.permission.allow_always"), "kind": "allow_always"})
	}
	options = append(options,
		map[string]interface{}{"optionId": "reject-once", "name": i18n.T("acp.permission.reject_once"), "kind": "reject_once"})
	if req.OfferAlways {
		options = append(options,
			map[string]interface{}{"optionId": "reject-always", "name": i18n.T("acp.permission.reject_always"), "kind": "reject_always"})
	}

	s.mu.Lock()
	dead := s.permDead
	s.mu.Unlock()
	if dead {
		return agentevents.PermissionDenyOnce, agentevents.ErrPermissionTimeout
	}
	ctx, cancel := s.runCtx, func() {}
	if s.permTimeout > 0 {
		ctx, cancel = context.WithTimeout(s.runCtx, s.permTimeout)
	}
	raw, err := s.acp.request(ctx, "session/request_permission", map[string]interface{}{
		"sessionId": s.sessionID,
		"toolCall":  toolCall,
		"options":   options,
	})
	cancel()
	if err != nil {
		// Minimal/wrapped clients may not implement the method at all;
		// surface the typed sentinel so the loop applies its unattended
		// fallback instead of denying every gated action.
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) && rpcErr.Code == CodeMethodNotFound {
			return agentevents.PermissionDenyOnce, agentevents.ErrPermissionUnsupported
		}
		// Our own bound expired while the run is still alive: the dialog is
		// unanswered (likely never rendered). Deny fail-safe and mark it dead
		// so the run doesn't stall again on every gated action.
		if errors.Is(err, context.DeadlineExceeded) && s.runCtx.Err() == nil {
			s.mu.Lock()
			s.permDead = true
			s.mu.Unlock()
			return agentevents.PermissionDenyOnce, agentevents.ErrPermissionTimeout
		}
		return agentevents.PermissionDenyOnce, err
	}
	var resp struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return agentevents.PermissionDenyOnce, err
	}
	if resp.Outcome.Outcome != "selected" {
		return agentevents.PermissionDenyOnce, nil
	}
	d, known := acpPermissionOptions[resp.Outcome.OptionID]
	if !known {
		return agentevents.PermissionDenyOnce, nil
	}
	// A persistent choice the dialog never offered must degrade to its
	// once-only form: never let a client answer widen what the caller can do.
	if !req.OfferAlways && d.Persistent() {
		if d.Allowed() {
			return agentevents.PermissionAllowOnce, nil
		}
		return agentevents.PermissionDenyOnce, nil
	}
	return d, nil
}

// closeOpen fails every still-open tool call — run on cancellation so the
// client never keeps a spinner on a call that will not finish.
func (s *acpSink) closeOpen(reason string) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.open))
	for id := range s.open {
		ids = append(ids, id)
	}
	s.open = map[string]bool{}
	s.mu.Unlock()
	for _, id := range ids {
		s.update(map[string]interface{}{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    id,
			"status":        "failed",
			"rawOutput":     map[string]interface{}{"error": reason},
		})
	}
}

// hasMessage reports whether the client already received assistant prose.
func (s *acpSink) hasMessage() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sawMessage
}

// toolCallPayload builds the session/update body for a tool call. full=true
// yields a complete tool_call (start or unseen-id close); full=false yields a
// tool_call_update carrying only the terminal fields.
func toolCallPayload(tc agentevents.ToolCall, full bool) map[string]interface{} {
	p := map[string]interface{}{
		"toolCallId": tc.ID,
		"status":     string(tc.Status.Normalize()),
	}
	if full {
		p["sessionUpdate"] = "tool_call"
		p["title"] = tc.Title
		p["kind"] = string(tc.Kind.Normalize())
		if tc.RawInput != "" {
			p["rawInput"] = map[string]interface{}{"args": tc.RawInput}
		}
	} else {
		p["sessionUpdate"] = "tool_call_update"
	}
	if len(tc.Locations) > 0 {
		locs := make([]map[string]interface{}, 0, len(tc.Locations))
		for _, l := range tc.Locations {
			loc := map[string]interface{}{"path": l.Path}
			if l.Line > 0 {
				loc["line"] = l.Line
			}
			locs = append(locs, loc)
		}
		p["locations"] = locs
	}
	if out := strings.TrimSpace(tc.Output); out != "" && !tc.OmitContent {
		if len(out) > acpToolContentCap {
			// Rune-safe cut: never split a UTF-8 sequence mid-byte.
			cut := acpToolContentCap
			for cut > 0 && !utf8.RuneStart(out[cut]) {
				cut--
			}
			out = out[:cut] + "\n… (output truncated for display; the agent received the full text)"
		}
		p["content"] = []map[string]interface{}{
			{"type": "content", "content": map[string]interface{}{"type": "text", "text": out}},
		}
	}
	return p
}
