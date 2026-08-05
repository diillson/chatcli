package rpcserve

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/diillson/chatcli/cli/agentevents"
)

// TestACPSinkRequestPermission_MethodNotFoundMapsUnsupported: a client that
// does not implement session/request_permission (wrapped/minimal ACP
// frontends) answers -32601 — the sink must surface the typed sentinel so the
// loop falls back to the legacy unattended contract instead of denying
// everything.
func TestACPSinkRequestPermission_MethodNotFoundMapsUnsupported(t *testing.T) {
	a := NewACP(&fakeBackend{}, "test")
	a.SetRequester(func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
		return nil, errf(CodeMethodNotFound, "no such method")
	})
	s := newACPSink(a, "sess", context.Background())
	allowed, err := s.RequestPermission(agentevents.ToolCall{ID: "c1", Name: "@coder"}, "reason")
	if allowed {
		t.Fatal("method-not-found must never allow")
	}
	if !errors.Is(err, agentevents.ErrPermissionUnsupported) {
		t.Fatalf("err = %v, want ErrPermissionUnsupported", err)
	}
}

// TestACPSinkRequestPermission_OtherErrorsStayOpaque: transport failures must
// NOT map to the unsupported sentinel — they deny fail-safe upstream.
func TestACPSinkRequestPermission_OtherErrorsStayOpaque(t *testing.T) {
	a := NewACP(&fakeBackend{}, "test")
	a.SetRequester(func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
		return nil, errf(CodeInternalError, "client disconnected")
	})
	s := newACPSink(a, "sess", context.Background())
	_, err := s.RequestPermission(agentevents.ToolCall{ID: "c1"}, "r")
	if err == nil || errors.Is(err, agentevents.ErrPermissionUnsupported) {
		t.Fatalf("internal error must stay opaque, got %v", err)
	}
}

// TestMCPInitializeTracksElicitation: the initialize handshake records
// whether the client declared the elicitation capability.
func TestMCPInitializeTracksElicitation(t *testing.T) {
	m := NewMCP(&fakeBackend{}, "chatcli", "test")
	m.initialize(json.RawMessage(`{"protocolVersion":"2025-03-26","capabilities":{"elicitation":{}}}`))
	if !m.clientElicits() {
		t.Fatal("elicitation capability must be recorded")
	}

	m2 := NewMCP(&fakeBackend{}, "chatcli", "test")
	m2.initialize(json.RawMessage(`{"protocolVersion":"2025-03-26","capabilities":{"sampling":{}}}`))
	if m2.clientElicits() {
		t.Fatal("absent elicitation capability must stay off")
	}
}

// TestMCPAgentTaskCarriesPermissions: with elicitation declared and the
// requester wired, agent_task/coder_task runs get a PermissionRequester that
// round-trips elicitation/create.
func TestMCPAgentTaskCarriesPermissions(t *testing.T) {
	f := &fakeBackend{}
	m := NewMCP(f, "chatcli", "test")
	var gotMethod string
	m.SetRequester(func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
		gotMethod = method
		return json.RawMessage(`{"action":"accept","content":{"approve":true}}`), nil
	})
	m.initialize(json.RawMessage(`{"capabilities":{"elicitation":{}}}`))

	params, _ := json.Marshal(map[string]interface{}{
		"name":      "agent_task",
		"arguments": map[string]interface{}{"task": "do x"},
	})
	if _, rpcErr := m.callTool(context.Background(), params); rpcErr != nil {
		t.Fatalf("callTool: %v", rpcErr)
	}
	f.mu.Lock()
	perms := f.lastOpts.Permissions
	f.mu.Unlock()
	if perms == nil {
		t.Fatal("agent_task must carry a PermissionRequester when the client declared elicitation")
	}
	allowed, err := perms.RequestPermission(agentevents.ToolCall{Name: "@coder", Title: "exec: rm x"}, "policy ask")
	if err != nil || !allowed {
		t.Fatalf("accept+approve must allow, got allowed=%v err=%v", allowed, err)
	}
	if gotMethod != "elicitation/create" {
		t.Fatalf("method = %q, want elicitation/create", gotMethod)
	}
}

// TestMCPNoElicitationNoPermissions: clients that did not declare
// elicitation (Devin and other wrapped CLIs) keep today's contract — no
// server→client request is ever attempted.
func TestMCPNoElicitationNoPermissions(t *testing.T) {
	f := &fakeBackend{}
	m := NewMCP(f, "chatcli", "test")
	m.SetRequester(func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
		t.Fatal("requester must never be consulted without the capability")
		return nil, nil
	})
	m.initialize(json.RawMessage(`{"capabilities":{}}`))

	params, _ := json.Marshal(map[string]interface{}{
		"name":      "coder_task",
		"arguments": map[string]interface{}{"task": "do y"},
	})
	if _, rpcErr := m.callTool(context.Background(), params); rpcErr != nil {
		t.Fatalf("callTool: %v", rpcErr)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastOpts.Permissions != nil {
		t.Fatal("no elicitation capability → Permissions must be nil")
	}
}

// TestMCPElicitOutcomes: decline/cancel/accept-without-approve deny;
// -32601 maps to the unsupported sentinel (legacy fallback upstream).
func TestMCPElicitOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		rpcErr  *RPCError
		allowed bool
		wantUns bool
	}{
		{"decline", `{"action":"decline"}`, nil, false, false},
		{"cancel", `{"action":"cancel"}`, nil, false, false},
		{"accept without approve", `{"action":"accept","content":{}}`, nil, false, false},
		{"accept approve false", `{"action":"accept","content":{"approve":false}}`, nil, false, false},
		{"accept approve true", `{"action":"accept","content":{"approve":true}}`, nil, true, false},
		{"method not found", ``, errf(CodeMethodNotFound, "nope"), false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMCP(&fakeBackend{}, "chatcli", "test")
			m.SetRequester(func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
				if tc.rpcErr != nil {
					return nil, tc.rpcErr
				}
				return json.RawMessage(tc.raw), nil
			})
			m.initialize(json.RawMessage(`{"capabilities":{"elicitation":{}}}`))
			pr := m.permissionsFor(context.Background())
			if pr == nil {
				t.Fatal("permissionsFor must return a requester")
			}
			allowed, err := pr.RequestPermission(agentevents.ToolCall{Name: "@coder"}, "r")
			if allowed != tc.allowed {
				t.Fatalf("allowed = %v, want %v", allowed, tc.allowed)
			}
			if tc.wantUns != errors.Is(err, agentevents.ErrPermissionUnsupported) {
				t.Fatalf("err = %v, wantUnsupported=%v", err, tc.wantUns)
			}
		})
	}
}
