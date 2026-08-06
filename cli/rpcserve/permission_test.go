package rpcserve

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

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

// TestACPSinkRequestDecision_FourOptionsWhenPersistable: with a persistable
// policy pattern (OfferAlways), the dialog must reach terminal parity — the
// four choices of the interactive security prompt, in the same order, using
// the ACP option-kind vocabulary.
func TestACPSinkRequestDecision_FourOptionsWhenPersistable(t *testing.T) {
	a := NewACP(&fakeBackend{}, "test")
	var captured map[string]interface{}
	a.SetRequester(func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
		captured = params.(map[string]interface{})
		return json.RawMessage(`{"outcome":{"outcome":"selected","optionId":"allow-always"}}`), nil
	})
	s := newACPSink(a, "sess", context.Background())
	d, err := s.RequestPermissionDecision(agentevents.PermissionRequest{
		Tool:        agentevents.ToolCall{ID: "c1", Name: "@coder", Title: "write: x"},
		Reason:      "policy ask",
		OfferAlways: true,
	})
	if err != nil {
		t.Fatalf("RequestPermissionDecision: %v", err)
	}
	if d != agentevents.PermissionAllowAlways {
		t.Fatalf("decision = %q, want allow_always", d)
	}
	opts, ok := captured["options"].([]map[string]interface{})
	if !ok {
		t.Fatalf("options missing or wrong type: %T", captured["options"])
	}
	wantKinds := []string{"allow_once", "allow_always", "reject_once", "reject_always"}
	wantIDs := []string{"allow-once", "allow-always", "reject-once", "reject-always"}
	if len(opts) != len(wantKinds) {
		t.Fatalf("got %d options, want %d", len(opts), len(wantKinds))
	}
	for i, o := range opts {
		if o["kind"] != wantKinds[i] || o["optionId"] != wantIDs[i] {
			t.Errorf("option %d = %v/%v, want %s/%s", i, o["optionId"], o["kind"], wantIDs[i], wantKinds[i])
		}
		if name, _ := o["name"].(string); name == "" {
			t.Errorf("option %d has empty name", i)
		}
	}
}

// TestACPSinkRequestDecision_TwoOptionsWhenNotPersistable: exec commands have
// no persistable pattern (the terminal hides allow/deny-always for them on
// purpose) — the dialog must NOT offer privilege the interactive flow denies.
func TestACPSinkRequestDecision_TwoOptionsWhenNotPersistable(t *testing.T) {
	a := NewACP(&fakeBackend{}, "test")
	var captured map[string]interface{}
	a.SetRequester(func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
		captured = params.(map[string]interface{})
		return json.RawMessage(`{"outcome":{"outcome":"selected","optionId":"allow-once"}}`), nil
	})
	s := newACPSink(a, "sess", context.Background())
	d, err := s.RequestPermissionDecision(agentevents.PermissionRequest{
		Tool: agentevents.ToolCall{ID: "c1", Name: "@coder", Title: "exec: rm x"},
	})
	if err != nil || d != agentevents.PermissionAllowOnce {
		t.Fatalf("decision = %q err = %v, want allow_once nil", d, err)
	}
	opts := captured["options"].([]map[string]interface{})
	if len(opts) != 2 {
		t.Fatalf("got %d options, want 2 (allow-once, reject-once)", len(opts))
	}
	if opts[0]["optionId"] != "allow-once" || opts[1]["optionId"] != "reject-once" {
		t.Fatalf("unexpected option ids: %v, %v", opts[0]["optionId"], opts[1]["optionId"])
	}
}

// TestACPSinkRequestDecision_Outcomes: every non-allow outcome must deny
// fail-safe; reject-always must surface as deny_always so the loop can
// persist the refusal.
func TestACPSinkRequestDecision_Outcomes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want agentevents.PermissionDecision
	}{
		{"reject always", `{"outcome":{"outcome":"selected","optionId":"reject-always"}}`, agentevents.PermissionDenyAlways},
		{"reject once", `{"outcome":{"outcome":"selected","optionId":"reject-once"}}`, agentevents.PermissionDenyOnce},
		{"cancelled", `{"outcome":{"outcome":"cancelled"}}`, agentevents.PermissionDenyOnce},
		{"unknown option id", `{"outcome":{"outcome":"selected","optionId":"mystery"}}`, agentevents.PermissionDenyOnce},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := NewACP(&fakeBackend{}, "test")
			a.SetRequester(func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
				return json.RawMessage(tc.raw), nil
			})
			s := newACPSink(a, "sess", context.Background())
			d, err := s.RequestPermissionDecision(agentevents.PermissionRequest{
				Tool:        agentevents.ToolCall{ID: "c1"},
				OfferAlways: true,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d != tc.want {
				t.Fatalf("decision = %q, want %q", d, tc.want)
			}
			if d.Allowed() {
				t.Fatal("non-allow outcome must not report Allowed()")
			}
		})
	}
}

// TestACPSinkRequestDecision_MethodNotFoundMapsUnsupported: the typed
// sentinel contract must hold on the decision surface exactly as it does on
// the boolean one — wrapped/minimal clients keep the legacy fallback.
func TestACPSinkRequestDecision_MethodNotFoundMapsUnsupported(t *testing.T) {
	a := NewACP(&fakeBackend{}, "test")
	a.SetRequester(func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
		return nil, errf(CodeMethodNotFound, "no such method")
	})
	s := newACPSink(a, "sess", context.Background())
	d, err := s.RequestPermissionDecision(agentevents.PermissionRequest{
		Tool: agentevents.ToolCall{ID: "c1"}, OfferAlways: true,
	})
	if d.Allowed() {
		t.Fatal("method-not-found must never allow")
	}
	if !errors.Is(err, agentevents.ErrPermissionUnsupported) {
		t.Fatalf("err = %v, want ErrPermissionUnsupported", err)
	}
}

// TestMCPElicitDecision_EnumSchema: with a persistable pattern the
// elicitation form must offer the four-way decision enum, and the accepted
// value must round-trip as the typed decision.
func TestMCPElicitDecision_EnumSchema(t *testing.T) {
	m := NewMCP(&fakeBackend{}, "chatcli", "test")
	var captured map[string]interface{}
	m.SetRequester(func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
		captured = params.(map[string]interface{})
		return json.RawMessage(`{"action":"accept","content":{"decision":"allow_always"}}`), nil
	})
	m.initialize(json.RawMessage(`{"capabilities":{"elicitation":{}}}`))
	pr := m.permissionsFor(context.Background())
	dec, ok := pr.(agentevents.PermissionDecider)
	if !ok {
		t.Fatal("MCP elicitation requester must implement PermissionDecider")
	}
	d, err := dec.RequestPermissionDecision(agentevents.PermissionRequest{
		Tool:        agentevents.ToolCall{Name: "@coder", Title: "write: x"},
		Reason:      "policy ask",
		OfferAlways: true,
	})
	if err != nil || d != agentevents.PermissionAllowAlways {
		t.Fatalf("decision = %q err = %v, want allow_always nil", d, err)
	}
	schema := captured["requestedSchema"].(map[string]interface{})
	props := schema["properties"].(map[string]interface{})
	decision, ok := props["decision"].(map[string]interface{})
	if !ok {
		t.Fatalf("decision property missing: %v", props)
	}
	enum, _ := decision["enum"].([]string)
	want := []string{"allow_once", "allow_always", "deny_once", "deny_always"}
	if len(enum) != len(want) {
		t.Fatalf("enum = %v, want %v", enum, want)
	}
	for i := range want {
		if enum[i] != want[i] {
			t.Fatalf("enum = %v, want %v", enum, want)
		}
	}
	req, _ := schema["required"].([]string)
	if len(req) != 1 || req[0] != "decision" {
		t.Fatalf("required = %v, want [decision]", req)
	}
}

// TestMCPElicitDecision_LegacySchemaWithoutOfferAlways: without a persistable
// pattern the wire must stay byte-compatible with today's boolean form —
// no enum, no new required fields.
func TestMCPElicitDecision_LegacySchemaWithoutOfferAlways(t *testing.T) {
	m := NewMCP(&fakeBackend{}, "chatcli", "test")
	var captured map[string]interface{}
	m.SetRequester(func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
		captured = params.(map[string]interface{})
		return json.RawMessage(`{"action":"accept","content":{"approve":true}}`), nil
	})
	m.initialize(json.RawMessage(`{"capabilities":{"elicitation":{}}}`))
	dec := m.permissionsFor(context.Background()).(agentevents.PermissionDecider)
	d, err := dec.RequestPermissionDecision(agentevents.PermissionRequest{
		Tool: agentevents.ToolCall{Name: "@coder", Title: "exec: rm x"},
	})
	if err != nil || d != agentevents.PermissionAllowOnce {
		t.Fatalf("decision = %q err = %v, want allow_once nil", d, err)
	}
	props := captured["requestedSchema"].(map[string]interface{})["properties"].(map[string]interface{})
	if _, hasApprove := props["approve"]; !hasApprove {
		t.Fatal("legacy path must keep the approve boolean schema")
	}
	if _, hasDecision := props["decision"]; hasDecision {
		t.Fatal("legacy path must not grow a decision enum")
	}
}

// TestMCPElicitDecision_Outcomes: decision-string handling must be lenient
// (legacy approve fallback) and fail-safe (decline/cancel/garbage deny).
func TestMCPElicitDecision_Outcomes(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		rpcErr  *RPCError
		want    agentevents.PermissionDecision
		wantUns bool
	}{
		{"deny always", `{"action":"accept","content":{"decision":"deny_always"}}`, nil, agentevents.PermissionDenyAlways, false},
		{"deny once", `{"action":"accept","content":{"decision":"deny_once"}}`, nil, agentevents.PermissionDenyOnce, false},
		{"allow once", `{"action":"accept","content":{"decision":"allow_once"}}`, nil, agentevents.PermissionAllowOnce, false},
		{"legacy approve true fallback", `{"action":"accept","content":{"approve":true}}`, nil, agentevents.PermissionAllowOnce, false},
		{"legacy approve false fallback", `{"action":"accept","content":{"approve":false}}`, nil, agentevents.PermissionDenyOnce, false},
		{"garbage decision denies", `{"action":"accept","content":{"decision":"sudo_everything"}}`, nil, agentevents.PermissionDenyOnce, false},
		{"decline", `{"action":"decline"}`, nil, agentevents.PermissionDenyOnce, false},
		{"cancel", `{"action":"cancel"}`, nil, agentevents.PermissionDenyOnce, false},
		{"method not found", ``, errf(CodeMethodNotFound, "nope"), agentevents.PermissionDenyOnce, true},
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
			dec := m.permissionsFor(context.Background()).(agentevents.PermissionDecider)
			d, err := dec.RequestPermissionDecision(agentevents.PermissionRequest{
				Tool: agentevents.ToolCall{Name: "@coder"}, OfferAlways: true,
			})
			if d != tc.want {
				t.Fatalf("decision = %q, want %q", d, tc.want)
			}
			if tc.wantUns != errors.Is(err, agentevents.ErrPermissionUnsupported) {
				t.Fatalf("err = %v, wantUnsupported=%v", err, tc.wantUns)
			}
		})
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

// TestMCPElicitDecision_TimeoutDeniesAndFailsFast: a client that declared the
// elicitation capability but never answers elicitation/create must not wedge
// the run until the outer context dies (the "stuck in the dark" hang). The
// round-trip is bounded by CHATCLI_MCP_PERMISSION_TIMEOUT; on expiry the
// decision denies fail-safe with the typed timeout sentinel, and subsequent
// requests in the same run fail fast without waiting again.
func TestMCPElicitDecision_TimeoutDeniesAndFailsFast(t *testing.T) {
	t.Setenv("CHATCLI_MCP_PERMISSION_TIMEOUT", "30ms")
	m := NewMCP(&fakeBackend{}, "chatcli", "test")
	calls := 0
	m.SetRequester(func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
		calls++
		<-ctx.Done() // client never answers; only the ctx bounds the wait
		return nil, ctx.Err()
	})
	m.initialize(json.RawMessage(`{"capabilities":{"elicitation":{}}}`))
	dec := m.permissionsFor(context.Background()).(agentevents.PermissionDecider)

	start := time.Now()
	d, err := dec.RequestPermissionDecision(agentevents.PermissionRequest{
		Tool: agentevents.ToolCall{Name: "@coder", Title: "exec: rm x"},
	})
	if d.Allowed() {
		t.Fatal("an unanswered dialog must never allow")
	}
	if !errors.Is(err, agentevents.ErrPermissionTimeout) {
		t.Fatalf("err = %v, want ErrPermissionTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout took %v, must be bounded by the configured 30ms", elapsed)
	}

	// Second request: the dialog is known-dead — fail fast, no second wait.
	start = time.Now()
	d, err = dec.RequestPermissionDecision(agentevents.PermissionRequest{
		Tool: agentevents.ToolCall{Name: "@coder", Title: "write: y"},
	})
	if d.Allowed() || !errors.Is(err, agentevents.ErrPermissionTimeout) {
		t.Fatalf("dead dialog must deny with timeout sentinel, got d=%q err=%v", d, err)
	}
	if calls != 1 {
		t.Fatalf("dead dialog must not be consulted again, requester calls = %d", calls)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Millisecond {
		t.Fatalf("fail-fast took %v", elapsed)
	}
}

// TestMCPElicitDecision_ContextCanceledStaysOpaque: the run's own context
// dying (client cancelled/killed the call) is NOT a dialog timeout — the
// error stays opaque so upstream treats it as a transport failure.
func TestMCPElicitDecision_ContextCanceledStaysOpaque(t *testing.T) {
	m := NewMCP(&fakeBackend{}, "chatcli", "test")
	m.SetRequester(func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	m.initialize(json.RawMessage(`{"capabilities":{"elicitation":{}}}`))
	ctx, cancel := context.WithCancel(context.Background())
	dec := m.permissionsFor(ctx).(agentevents.PermissionDecider)
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	d, err := dec.RequestPermissionDecision(agentevents.PermissionRequest{
		Tool: agentevents.ToolCall{Name: "@coder"},
	})
	if d.Allowed() {
		t.Fatal("cancelled run must never allow")
	}
	if errors.Is(err, agentevents.ErrPermissionTimeout) || errors.Is(err, agentevents.ErrPermissionUnsupported) {
		t.Fatalf("run cancellation must stay an opaque transport error, got %v", err)
	}
}

// TestMCPPermissionsElicitationKillSwitch: CHATCLI_MCP_ELICITATION=off must
// disable the bridge even when the client declares the capability — the
// escape hatch for clients that declare elicitation but never render it.
func TestMCPPermissionsElicitationKillSwitch(t *testing.T) {
	t.Setenv("CHATCLI_MCP_ELICITATION", "off")
	m := NewMCP(&fakeBackend{}, "chatcli", "test")
	m.SetRequester(func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
		t.Fatal("requester must never be consulted with the kill switch on")
		return nil, nil
	})
	m.initialize(json.RawMessage(`{"capabilities":{"elicitation":{}}}`))
	if pr := m.permissionsFor(context.Background()); pr != nil {
		t.Fatalf("permissionsFor = %T, want nil with CHATCLI_MCP_ELICITATION=off", pr)
	}
}

// TestMCPPermissionTimeoutParsing: env accepts Go durations and plain
// seconds; 0/off disable the bound; garbage falls back to the default.
func TestMCPPermissionTimeoutParsing(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", mcpPermissionTimeoutDefault},
		{"90s", 90 * time.Second},
		{"2m", 2 * time.Minute},
		{"45", 45 * time.Second},
		{"0", 0},
		{"off", 0},
		{"garbage", mcpPermissionTimeoutDefault},
		{"-5s", mcpPermissionTimeoutDefault},
	}
	for _, tc := range cases {
		t.Run("env="+tc.env, func(t *testing.T) {
			t.Setenv("CHATCLI_MCP_PERMISSION_TIMEOUT", tc.env)
			if got := mcpPermissionTimeout(); got != tc.want {
				t.Fatalf("mcpPermissionTimeout() = %v, want %v", got, tc.want)
			}
		})
	}
}
