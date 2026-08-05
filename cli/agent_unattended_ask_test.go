/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agentevents"
	"go.uber.org/zap"
)

// permRecorder is a sinkRecorder that also implements PermissionRequester,
// with a scripted decision.
type permRecorder struct {
	sinkRecorder
	grant   bool
	err     error
	asked   []agentevents.ToolCall
	reasons []string
}

func (p *permRecorder) RequestPermission(tc agentevents.ToolCall, reason string) (bool, error) {
	p.asked = append(p.asked, tc)
	p.reasons = append(p.reasons, reason)
	return p.grant, p.err
}

func newUnattendedAgent(sink agentevents.Sink) (*AgentMode, *ChatCLI) {
	c := &ChatCLI{unattended: true}
	a := NewAgentMode(c, zap.NewNop())
	a.events = sink
	return a, c
}

// TestUnattendedAskBlocked_DeniedByClient: with a PermissionRequester
// installed (ACP), a policy "ask" rule must surface the client's dialog —
// and a denial must block the action, tell the model, and close the
// tool_call for the client.
func TestUnattendedAskBlocked_DeniedByClient(t *testing.T) {
	rec := &permRecorder{grant: false}
	a, c := newUnattendedAgent(rec)

	blocked := a.unattendedAskBlocked("@coder", `{"cmd":"exec","args":{"cmd":"docker rm x"}}`, func(string) {})
	if !blocked {
		t.Fatal("denied ask rule must block the action")
	}
	if len(rec.asked) != 1 {
		t.Fatalf("requester consulted %d times, want 1", len(rec.asked))
	}
	found := false
	for _, m := range c.history {
		if strings.Contains(m.Content, "DENIED") {
			found = true
		}
	}
	if !found {
		t.Fatal("denial must reach the model through history so it can replan")
	}
	if len(rec.ends) != 1 {
		t.Fatalf("expected 1 blocked tool_call event for the client, got %d", len(rec.ends))
	}
	if !rec.ends[0].IsError {
		t.Error("blocked tool_call must be reported as error")
	}
}

// TestUnattendedAskBlocked_AllowedByClient: an explicit approval in the
// client dialog runs the action.
func TestUnattendedAskBlocked_AllowedByClient(t *testing.T) {
	rec := &permRecorder{grant: true}
	a, c := newUnattendedAgent(rec)

	blocked := a.unattendedAskBlocked("@coder", `{"cmd":"write","args":{"file":"x"}}`, func(string) {})
	if blocked {
		t.Fatal("approved ask rule must not block")
	}
	if len(rec.asked) != 1 {
		t.Fatalf("requester consulted %d times, want 1", len(rec.asked))
	}
	if len(c.history) != 0 {
		t.Fatalf("approval must not pollute history, got %d messages", len(c.history))
	}
}

// TestUnattendedAskBlocked_NoRequester: without any requester (gateway
// daemon, MCP client without elicitation) the historical unattended contract
// holds — ask auto-approves.
func TestUnattendedAskBlocked_NoRequester(t *testing.T) {
	a, c := newUnattendedAgent(&sinkRecorder{}) // sink without PermissionRequester
	if a.unattendedAskBlocked("@coder", `{"cmd":"exec"}`, func(string) {}) {
		t.Fatal("without a requester, ask must keep the legacy auto-approve")
	}
	if len(c.history) != 0 {
		t.Fatal("legacy auto-approve must not touch history")
	}
}

// TestUnattendedAskBlocked_UnsupportedClientFallsBack: a client that answers
// "method not found" (wrapped/minimal ACP clients) falls back to the legacy
// auto-approve instead of bricking every ask-gated action.
func TestUnattendedAskBlocked_UnsupportedClientFallsBack(t *testing.T) {
	rec := &permRecorder{err: agentevents.ErrPermissionUnsupported}
	a, c := newUnattendedAgent(rec)
	if a.unattendedAskBlocked("@coder", `{"cmd":"exec"}`, func(string) {}) {
		t.Fatal("unsupported permission method must fall back to legacy auto-approve")
	}
	if len(c.history) != 0 {
		t.Fatal("fallback must not touch history")
	}
}

// TestUnattendedAskBlocked_TransportErrorDenies: any other requester failure
// denies (fail-safe) — the user may have wanted to say no.
func TestUnattendedAskBlocked_TransportErrorDenies(t *testing.T) {
	rec := &permRecorder{err: errFixed("client disconnected")}
	a, _ := newUnattendedAgent(rec)
	if !a.unattendedAskBlocked("@coder", `{"cmd":"exec"}`, func(string) {}) {
		t.Fatal("transport error must deny fail-safe")
	}
}

// TestRequestActionPermission_UnsupportedNotAsked: ErrPermissionUnsupported
// means "no dialog exists", which callers must treat as not-consulted — the
// dangerous-exec guard then blocks in-band exactly like the MCP contract.
func TestRequestActionPermission_UnsupportedNotAsked(t *testing.T) {
	rec := &permRecorder{err: agentevents.ErrPermissionUnsupported}
	a, _ := newUnattendedAgent(rec)
	allowed, asked := a.requestActionPermission(agentevents.ToolCall{Name: "@coder"}, "r")
	if allowed || asked {
		t.Fatalf("unsupported must report not-consulted, got allowed=%v asked=%v", allowed, asked)
	}
}

// TestRequestActionPermission_FallbackRequester: runs without an events sink
// (MCP agent_task/coder_task) reach the per-run requester installed on the
// ChatCLI (elicitation bridge).
func TestRequestActionPermission_FallbackRequester(t *testing.T) {
	rec := &permRecorder{grant: true}
	c := &ChatCLI{unattended: true, rpcPermissions: rec}
	a := NewAgentMode(c, zap.NewNop()) // a.events stays nil
	allowed, asked := a.requestActionPermission(agentevents.ToolCall{Name: "@coder"}, "r")
	if !allowed || !asked {
		t.Fatalf("fallback requester must be consulted, got allowed=%v asked=%v", allowed, asked)
	}
	if len(rec.asked) != 1 {
		t.Fatalf("requester consulted %d times, want 1", len(rec.asked))
	}
}
