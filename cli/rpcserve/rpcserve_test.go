package rpcserve

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agentevents"
)

// fakeBackend echoes prompts and records the last call details. It
// deliberately does NOT implement MCPToolProxy — proxyFake adds that
// capability, so both handler paths stay pinned.
type fakeBackend struct {
	noLLM       bool
	mu          sync.Mutex
	lastSession string
	reply       string
	err         error
	lastTask    string
	lastTool    string
	lastArgs    string
	lastOpts    RunOpts
	blockAgent  chan struct{} // when non-nil, AgentStream blocks until ctx cancel or close
	// Structured-bridge knobs:
	noMessage   bool // skip Events.Message so the final-reply fallback engages
	askDanger   bool // exercise PermissionRequester mid-run; result in permGranted
	permGranted bool
	lastCmd     string // last RunCommand line
	cmdOut      string
	cmdErr      error
}

func (f *fakeBackend) HasLLM() bool { return !f.noLLM }

func (f *fakeBackend) Prompt(_ context.Context, session, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSession = session
	if f.err != nil {
		return "", f.err
	}
	if f.reply != "" {
		return f.reply, nil
	}
	return "echo:" + text, nil
}

func (f *fakeBackend) PromptWith(ctx context.Context, session, text string, opts RunOpts) (string, error) {
	f.mu.Lock()
	f.lastOpts = opts
	f.mu.Unlock()
	return f.Prompt(ctx, session, text)
}

func (f *fakeBackend) Agent(_ context.Context, session, task string, opts RunOpts) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSession, f.lastTask, f.lastOpts = session, task, opts
	return "agent-ran:" + task, nil
}

func (f *fakeBackend) Coder(_ context.Context, session, task string, opts RunOpts) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSession, f.lastTask, f.lastOpts = session, task, opts
	return "coder-ran:" + task, nil
}

func (f *fakeBackend) AgentStream(ctx context.Context, session, task string, opts RunOpts) (string, error) {
	f.mu.Lock()
	f.lastSession, f.lastTask = session, task
	block := f.blockAgent
	f.mu.Unlock()
	// Structured path: drive the canonical event sequence the real loop
	// emits — thought → tool_call → (block) → tool_call_update → plan →
	// message — so the ACP frame translation stays pinned.
	if opts.Events != nil {
		opts.Events.Thought("thinking about: " + task)
		tc := agentevents.ToolCall{
			ID: "call-1", Name: "@coder", Title: "Reading: main.go",
			Kind: agentevents.KindRead, Status: agentevents.StatusInProgress,
			RawInput:  `{"cmd":"read"}`,
			Locations: []agentevents.Location{{Path: "main.go"}},
		}
		opts.Events.ToolStart(tc)
		if f.askDanger {
			if pr, ok := opts.Events.(agentevents.PermissionRequester); ok {
				granted, _ := pr.RequestPermission(tc, "dangerous command")
				f.mu.Lock()
				f.permGranted = granted
				f.mu.Unlock()
			}
		}
		if block != nil {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-block:
			}
		}
		tc.Status = agentevents.StatusCompleted
		tc.Output = "line1\nline2"
		tc.OmitContent = true // successful read: no content dump
		opts.Events.ToolEnd(tc)
		opts.Events.PlanUpdate(agentevents.Plan{Entries: []agentevents.PlanEntry{
			{Content: "read files", Priority: "medium", Status: "completed"},
			{Content: "answer", Priority: "medium", Status: "in_progress"},
		}})
		if !f.noMessage {
			opts.Events.Message("all done: " + task)
		}
		return "agent-ran:" + task, nil
	}
	if opts.Emit != nil {
		opts.Emit("working on: " + task)
	}
	if block != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-block:
		}
	}
	if opts.Emit != nil {
		opts.Emit("done: " + task)
	}
	return "agent-ran:" + task, nil
}

func (f *fakeBackend) CoderStream(ctx context.Context, session, task string, opts RunOpts) (string, error) {
	f.mu.Lock()
	f.lastSession, f.lastTask = session, task
	f.mu.Unlock()
	if opts.Events != nil {
		tc := agentevents.ToolCall{
			ID: "call-c1", Name: "@coder", Title: "exec: go test",
			Kind: agentevents.KindExecute, Status: agentevents.StatusInProgress,
		}
		opts.Events.ToolStart(tc)
		tc.Status = agentevents.StatusCompleted
		tc.Output = "ok"
		opts.Events.ToolEnd(tc)
		if !f.noMessage {
			opts.Events.Message("coder done: " + task)
		}
		return "coder-ran:" + task, nil
	}
	if opts.Emit != nil {
		opts.Emit("coder-line")
	}
	return "coder-ran:" + task, nil
}

func (f *fakeBackend) ACPCommands() []CommandInfo {
	return []CommandInfo{
		{Name: "chat", Description: "chat mode"},
		{Name: "agent", Description: "agent mode"},
		{Name: "coder", Description: "coder mode"},
		{Name: "config", Description: "show config", InputHint: "arguments"},
		{Name: "model", Description: "show model"},
	}
}

func (f *fakeBackend) RunCommand(_ context.Context, session, line string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSession, f.lastCmd = session, line
	if f.cmdErr != nil {
		return "", f.cmdErr
	}
	if f.cmdOut != "" {
		return f.cmdOut, nil
	}
	return "ran:" + line, nil
}

func (f *fakeBackend) Tools() []ToolInfo {
	return []ToolInfo{
		{Name: "read", Description: "Read a file", Usage: "read <path>", ReadOnly: true},
		{Name: "coder", Description: "Edit and run", Usage: "coder {json}", ReadOnly: false},
	}
}

func (f *fakeBackend) CallTool(_ context.Context, name, args string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastTool, f.lastArgs = name, args
	return "tool:" + name + ":" + args, nil
}

// proxyFake is a fakeBackend that also implements MCPToolProxy (the MCP-hub
// capability), recording the last proxied call.
type proxyFake struct {
	fakeBackend
	mcpTools      []MCPProxyToolInfo
	lastMCPTool   string
	lastMCPArgs   string
	lastSessionOp string
}

func (f *proxyFake) MCPProxyTools() []MCPProxyToolInfo { return f.mcpTools }

func (f *proxyFake) CallMCPProxyTool(_ context.Context, name string, args json.RawMessage) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastMCPTool, f.lastMCPArgs = name, string(args)
	return "mcp-proxied:" + name, nil
}

// ManageSession implements SessionBackend on the full-capability fake.
func (f *proxyFake) ManageSession(_ context.Context, action, session, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSessionOp = action + ":" + session + ":" + name
	return "session-managed:" + action, nil
}

func (f *fakeBackend) Skills() []SkillInfo {
	return []SkillInfo{{Name: "deploy-checklist", Description: "Pre-deploy checks"}}
}

func (f *fakeBackend) SkillContent(name string) (string, error) {
	if name != "deploy-checklist" {
		return "", errUnknownSkill
	}
	return "# Deploy checklist\n1. run tests", nil
}

func (f *fakeBackend) ProvidersJSON() (string, error) {
	return `{"active_provider":"OPENAI","providers":[{"name":"OPENAI"},{"name":"DEVIN"}]}`, nil
}

type errStr string

func (e errStr) Error() string { return string(e) }

const errUnknownSkill = errStr("unknown skill")

// runLines feeds request lines through a Server and returns the decoded
// responses sorted by id (dispatch is concurrent, so wire order is not
// guaranteed).
func runLines(t *testing.T, handler handlerFunc, lines ...string) []Response {
	t.Helper()
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out syncBuffer
	srv := NewServer(in, &out, handler)
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var resps []Response
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l == "" {
			continue
		}
		var r Response
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("decode response %q: %v", l, err)
		}
		if len(r.ID) == 0 {
			continue // server-initiated notification, not a response
		}
		resps = append(resps, r)
	}
	sort.Slice(resps, func(i, j int) bool { return string(resps[i].ID) < string(resps[j].ID) })
	return resps
}

// syncBuffer is a goroutine-safe strings.Builder for the concurrent writer.
type syncBuffer struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

func TestJSONRPC_NotificationGetsNoResponse(t *testing.T) {
	h := func(context.Context, string, json.RawMessage) (interface{}, *RPCError) { return "ok", nil }
	resps := runLines(t, h, `{"jsonrpc":"2.0","method":"foo"}`)
	if len(resps) != 0 {
		t.Errorf("notification should produce no response, got %d", len(resps))
	}
}

func TestJSONRPC_ParseError(t *testing.T) {
	h := func(context.Context, string, json.RawMessage) (interface{}, *RPCError) { return nil, nil }
	in := strings.NewReader("{not json\n")
	var out syncBuffer
	srv := NewServer(in, &out, h)
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !strings.Contains(out.String(), `"code":-32700`) {
		t.Fatalf("expected parse error, got %s", out.String())
	}
}

func TestJSONRPC_ConcurrentDispatchDoesNotBlockReads(t *testing.T) {
	// A slow request must not prevent a later request from being handled:
	// the second (fast) call releases the first (blocked) one.
	release := make(chan struct{})
	h := func(_ context.Context, method string, _ json.RawMessage) (interface{}, *RPCError) {
		if method == "slow" {
			<-release
			return "slow-done", nil
		}
		close(release)
		return "fast-done", nil
	}
	resps := runLines(t, h,
		`{"jsonrpc":"2.0","id":1,"method":"slow"}`,
		`{"jsonrpc":"2.0","id":2,"method":"fast"}`,
	)
	if len(resps) != 2 {
		t.Fatalf("expected both responses, got %d", len(resps))
	}
}

func TestMCP_InitializeNegotiatesVersion(t *testing.T) {
	m := NewMCP(&fakeBackend{}, "chatcli", "1.0.0")
	resps := runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
	)
	body, _ := json.Marshal(resps[0].Result)
	if !strings.Contains(string(body), "2024-11-05") {
		t.Errorf("must echo a supported client version, got %s", body)
	}
	if !strings.Contains(string(body), `"prompts"`) {
		t.Errorf("must advertise the prompts capability, got %s", body)
	}
}

func TestMCP_ToolsListFullSurface(t *testing.T) {
	m := NewMCP(&fakeBackend{}, "chatcli", "1.0.0")
	resps := runLines(t, m.Handle, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	body, _ := json.Marshal(resps[0].Result)
	for _, want := range []string{"ask_chatcli", "agent_task", "coder_task", "list_providers", `"read"`, `"coder"`, "readOnlyHint", "provider", "quality"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("tools/list missing %q: %s", want, body)
		}
	}
	// json.Marshal escapes '<' as \u003c, so assert on the marker + name.
	if !strings.Contains(string(body), "Usage:") || !strings.Contains(string(body), "u003cpath") {
		t.Errorf("plugin usage must be embedded in the description: %s", body)
	}
	// Strict MCP clients (Claude) validate inputSchema.required as an
	// array; a nil Go slice marshaling to null breaks the connection.
	if strings.Contains(string(body), `"required":null`) {
		t.Errorf("inputSchema.required must never be null: %s", body)
	}
}

func TestMCP_RoutingOptionsPropagate(t *testing.T) {
	be := &fakeBackend{}
	m := NewMCP(be, "chatcli", "1.0.0")
	runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"agent_task","arguments":{"task":"t","provider":"DEVIN","model":"gpt-5.6-sol","quality":{"CHATCLI_QUALITY_ENABLED":"true"}}}}`,
	)
	if be.lastOpts.Provider != "DEVIN" || be.lastOpts.Model != "gpt-5.6-sol" {
		t.Errorf("provider/model routing not propagated: %+v", be.lastOpts)
	}
	if be.lastOpts.Quality["CHATCLI_QUALITY_ENABLED"] != "true" {
		t.Errorf("quality toggles not propagated: %+v", be.lastOpts.Quality)
	}
}

func TestMCP_ListProvidersAndPluginDispatch(t *testing.T) {
	be := &fakeBackend{}
	m := NewMCP(be, "chatcli", "1.0.0")
	r := runLines(t, m.Handle, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_providers","arguments":{}}}`)
	if b, _ := json.Marshal(r[0].Result); !strings.Contains(string(b), "DEVIN") {
		t.Errorf("list_providers wrong: %s", b)
	}
	r = runLines(t, m.Handle, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read","arguments":{"args":"main.go"}}}`)
	if b, _ := json.Marshal(r[0].Result); !strings.Contains(string(b), "tool:read:main.go") {
		t.Errorf("plugin dispatch wrong: %s", b)
	}
}

func TestMCP_SkillsAsPrompts(t *testing.T) {
	m := NewMCP(&fakeBackend{}, "chatcli", "1.0.0")
	r := runLines(t, m.Handle, `{"jsonrpc":"2.0","id":1,"method":"prompts/list","params":{}}`)
	if b, _ := json.Marshal(r[0].Result); !strings.Contains(string(b), "deploy-checklist") {
		t.Errorf("prompts/list missing skill: %s", b)
	}
	r = runLines(t, m.Handle, `{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{"name":"deploy-checklist"}}`)
	if b, _ := json.Marshal(r[0].Result); !strings.Contains(string(b), "Deploy checklist") {
		t.Errorf("prompts/get missing content: %s", b)
	}
	r = runLines(t, m.Handle, `{"jsonrpc":"2.0","id":3,"method":"prompts/get","params":{"name":"nope"}}`)
	if r[0].Error == nil {
		t.Error("unknown prompt must error")
	}
}

// TestMCP_NoProviderHidesLLMTools pins the no-credentials surface: without
// an LLM provider the harness tools disappear from tools/list (the caller's
// model drives the direct tools itself), while every plugin tool and
// list_providers remain advertised.
func TestMCP_NoProviderHidesLLMTools(t *testing.T) {
	m := NewMCP(&fakeBackend{noLLM: true}, "chatcli", "1.0.0")
	resps := runLines(t, m.Handle, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	body, _ := json.Marshal(resps[0].Result)
	for _, gone := range []string{"ask_chatcli", "agent_task", "coder_task"} {
		if strings.Contains(string(body), `"`+gone+`"`) {
			t.Errorf("LLM-backed tool %q must be hidden without a provider: %s", gone, body)
		}
	}
	for _, keep := range []string{"list_providers", `"read"`, `"coder"`} {
		if !strings.Contains(string(body), keep) {
			t.Errorf("direct tool %q must remain without a provider: %s", keep, body)
		}
	}
}

// proxyBackend returns a fake advertising two proxied MCP tools with a real
// origin schema, exercising the hub/aggregator surface.
func proxyBackend() *proxyFake {
	return &proxyFake{mcpTools: []MCPProxyToolInfo{
		{
			Name:        "list_regions",
			Server:      "aws",
			Description: "List AWS regions",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"partition": map[string]interface{}{"type": "string"}},
				"required":   []string{"partition"},
			},
			ReadOnly: true,
		},
		{Name: "no_schema_tool", Server: "other", Description: "Schema-less"},
	}}
}

// TestMCP_ProxiedToolsListedWithOriginSchema pins the MCP-hub contract:
// tools discovered from servers ChatCLI is connected to are re-exported
// under their mcp_ names, keeping the origin JSON Schema (not the plugin
// args-string envelope) and the origin readOnlyHint; schema-less tools get
// a valid empty object schema.
func TestMCP_ProxiedToolsListedWithOriginSchema(t *testing.T) {
	m := NewMCP(proxyBackend(), "chatcli", "1.0.0")
	resps := runLines(t, m.Handle, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	body, _ := json.Marshal(resps[0].Result)
	s := string(body)
	for _, want := range []string{`"mcp_list_regions"`, "[MCP:aws] List AWS regions", `"partition"`, `"required":["partition"]`, `"mcp_no_schema_tool"`} {
		if !strings.Contains(s, want) {
			t.Errorf("tools/list missing %q: %s", want, s)
		}
	}
	if strings.Contains(s, `"required":null`) {
		t.Errorf("inputSchema.required must never be null: %s", s)
	}
}

// TestMCP_ProxiedToolCallForwardsRawArguments pins that a proxied call
// carries the caller's arguments object verbatim — no args-string envelope.
func TestMCP_ProxiedToolCallForwardsRawArguments(t *testing.T) {
	be := proxyBackend()
	m := NewMCP(be, "chatcli", "1.0.0")
	r := runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mcp_list_regions","arguments":{"partition":"aws","nested":{"deep":true}}}}`,
	)
	if b, _ := json.Marshal(r[0].Result); !strings.Contains(string(b), "mcp-proxied:mcp_list_regions") {
		t.Errorf("proxied dispatch wrong: %s", b)
	}
	if be.lastMCPTool != "mcp_list_regions" {
		t.Errorf("tool name not forwarded: %q", be.lastMCPTool)
	}
	if !strings.Contains(be.lastMCPArgs, `"nested":{"deep":true}`) {
		t.Errorf("arguments must be forwarded verbatim, got: %s", be.lastMCPArgs)
	}
}

// TestMCP_InitializeAdvertisesToolsListChanged pins tools.listChanged: true
// when the backend proxies MCP tools (that catalog is dynamic — it grows as
// ChatCLI's own client connects), false for a backend without the hub
// capability (static native surface).
func TestMCP_InitializeAdvertisesToolsListChanged(t *testing.T) {
	m := NewMCP(proxyBackend(), "chatcli", "1.0.0")
	resps := runLines(t, m.Handle, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	body, _ := json.Marshal(resps[0].Result)
	if !strings.Contains(string(body), `"tools":{"listChanged":true}`) {
		t.Errorf("proxy-capable backend must advertise tools.listChanged=true: %s", body)
	}

	m = NewMCP(&fakeBackend{}, "chatcli", "1.0.0")
	resps = runLines(t, m.Handle, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	body, _ = json.Marshal(resps[0].Result)
	if !strings.Contains(string(body), `"tools":{"listChanged":false}`) {
		t.Errorf("slim backend must advertise tools.listChanged=false: %s", body)
	}
	if ToolsListChangedMethod != "notifications/tools/list_changed" {
		t.Errorf("ToolsListChangedMethod drifted: %q", ToolsListChangedMethod)
	}
}

// TestMCP_SlimBackendIgnoresMCPPrefix pins the no-capability path: a backend
// without MCPToolProxy routes mcp_-prefixed names to the plugin dispatcher
// instead of panicking or dropping the call.
func TestMCP_SlimBackendIgnoresMCPPrefix(t *testing.T) {
	be := &fakeBackend{}
	m := NewMCP(be, "chatcli", "1.0.0")
	r := runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mcp_x","arguments":{"args":"y"}}}`,
	)
	if b, _ := json.Marshal(r[0].Result); !strings.Contains(string(b), "tool:mcp_x:y") {
		t.Errorf("slim backend must fall through to CallTool: %s", b)
	}
	resps := runLines(t, m.Handle, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if b, _ := json.Marshal(resps[0].Result); strings.Contains(string(b), `"mcp_`) {
		t.Errorf("slim backend must not advertise proxied tools: %s", b)
	}
}

// TestMCP_ManageSession pins the session-management surface: advertised and
// dispatched only when the backend implements SessionBackend, with the
// session default applied and action required.
func TestMCP_ManageSession(t *testing.T) {
	be := proxyBackend()
	m := NewMCP(be, "chatcli", "1.0.0")

	resps := runLines(t, m.Handle, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if b, _ := json.Marshal(resps[0].Result); !strings.Contains(string(b), `"manage_session"`) {
		t.Errorf("session-capable backend must advertise manage_session: %s", b)
	}

	r := runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"manage_session","arguments":{"action":"save","name":"reunion"}}}`,
	)
	if b, _ := json.Marshal(r[0].Result); !strings.Contains(string(b), "session-managed:save") {
		t.Errorf("manage_session dispatch wrong: %s", b)
	}
	if be.lastSessionOp != "save:mcp:reunion" {
		t.Errorf("action/session/name not propagated (session must default to mcp): %q", be.lastSessionOp)
	}

	r = runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"manage_session","arguments":{}}}`,
	)
	if r[0].Error == nil || r[0].Error.Code != CodeInvalidParams {
		t.Errorf("missing action must be invalid params, got %+v", r[0])
	}

	// A backend without the capability neither advertises nor dispatches it.
	slim := NewMCP(&fakeBackend{}, "chatcli", "1.0.0")
	resps = runLines(t, slim.Handle, `{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{}}`)
	if b, _ := json.Marshal(resps[0].Result); strings.Contains(string(b), `"manage_session"`) {
		t.Errorf("slim backend must not advertise manage_session: %s", b)
	}
	r = runLines(t, slim.Handle,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"manage_session","arguments":{"action":"list","args":"x"}}}`,
	)
	if b, _ := json.Marshal(r[0].Result); !strings.Contains(string(b), "tool:manage_session") {
		t.Errorf("slim backend must fall through to the plugin dispatcher: %s", b)
	}
}

func TestMCP_ChatMissingPrompt(t *testing.T) {
	m := NewMCP(&fakeBackend{}, "chatcli", "1.0.0")
	resps := runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_chatcli","arguments":{}}}`,
	)
	if len(resps) != 1 || resps[0].Error == nil || resps[0].Error.Code != CodeInvalidParams {
		t.Fatalf("expected invalid params, got %+v", resps)
	}
}

// acpSessionID drives session/new and returns the id.
func acpNewSession(t *testing.T, a *ACP) string {
	t.Helper()
	resps := runLines(t, a.Handle, `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/tmp"}}`)
	body, _ := json.Marshal(resps[0].Result)
	var nr struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(body, &nr)
	if nr.SessionID == "" {
		t.Fatal("session/new should return a sessionId")
	}
	if !strings.Contains(string(body), "availableModes") {
		t.Fatalf("session/new must advertise modes: %s", body)
	}
	if !strings.Contains(string(body), `"currentModeId":"coder"`) {
		t.Fatalf("new sessions must default to coder mode: %s", body)
	}
	return nr.SessionID
}

// acpAgentSession creates a session and switches it to agent mode — tests
// that pin the AgentStream path opt in explicitly, since the default is coder.
func acpAgentSession(t *testing.T, a *ACP) string {
	t.Helper()
	sid := acpNewSession(t, a)
	resps := runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":90,"method":"session/set_mode","params":{"sessionId":"`+sid+`","modeId":"agent"}}`)
	if resps[0].Error != nil {
		t.Fatalf("set_mode agent failed: %+v", resps[0].Error)
	}
	return sid
}

func notesRecorder(a *ACP) (*[]string, *sync.Mutex) {
	var mu sync.Mutex
	notes := &[]string{}
	a.SetNotifier(func(method string, params interface{}) error {
		b, _ := json.Marshal(params)
		mu.Lock()
		*notes = append(*notes, method+":"+string(b))
		mu.Unlock()
		return nil
	})
	return notes, &mu
}

func TestACP_AgentStreamsStructuredUpdates(t *testing.T) {
	be := &fakeBackend{}
	a := NewACP(be, "1.0.0")
	notes, nmu := notesRecorder(a)
	sid := acpAgentSession(t, a)

	pr := runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"build it"}]}}`,
	)
	res, _ := json.Marshal(pr[0].Result)
	if !strings.Contains(string(res), "end_turn") {
		t.Errorf("expected end_turn, got %s", res)
	}
	nmu.Lock()
	frames := append([]string(nil), *notes...)
	nmu.Unlock()
	joined := strings.Join(frames, "\n")

	// Ordered structured frames: thought → tool_call(in_progress) →
	// tool_call_update(completed) → plan → final answer chunk. Each entry
	// lists the markers ONE frame must carry; frames must appear in order.
	frameMarkers := [][]string{
		{"agent_thought_chunk", "thinking about: build it"},
		{`"sessionUpdate":"tool_call"`, `"status":"in_progress"`, `"toolCallId":"call-1"`},
		{"tool_call_update", `"status":"completed"`},
		{`"sessionUpdate":"plan"`},
		{"agent_message_chunk", "all done: build it"},
	}
	fi := 0
	for _, want := range frameMarkers {
		found := false
		for ; fi < len(frames); fi++ {
			match := true
			for _, m := range want {
				if !strings.Contains(frames[fi], m) {
					match = false
					break
				}
			}
			if match {
				found = true
				fi++
				break
			}
		}
		if !found {
			t.Fatalf("frame with markers %v missing or out of order in:\n%s", want, joined)
		}
	}
	// No raw line-dump scraping and no file-content dump for the read.
	if strings.Contains(joined, "working on:") {
		t.Errorf("legacy Emit scraping must be off on structured runs: %s", joined)
	}
	if strings.Contains(joined, "line1") {
		t.Errorf("successful read output must not become chat content: %s", joined)
	}
	if !strings.Contains(joined, `"path":"main.go"`) {
		t.Errorf("tool locations must be forwarded: %s", joined)
	}
	if be.lastTask != "build it" {
		t.Errorf("task not propagated: %q", be.lastTask)
	}
}

func TestACP_FinalReplyFallback(t *testing.T) {
	// The loop emitted no Message — the backend's returned reply must still
	// reach the client as an agent_message_chunk.
	be := &fakeBackend{noMessage: true}
	a := NewACP(be, "1.0.0")
	notes, nmu := notesRecorder(a)
	sid := acpNewSession(t, a)

	runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"quiet"}]}}`,
	)
	nmu.Lock()
	joined := strings.Join(*notes, "\n")
	nmu.Unlock()
	if !strings.Contains(joined, "coder-ran:quiet") {
		t.Errorf("fallback final reply missing (default mode drives CoderStream): %s", joined)
	}
}

func TestACP_NewSessionSendsAvailableCommands(t *testing.T) {
	be := &fakeBackend{}
	a := NewACP(be, "1.0.0")
	// Wire the notifier to the same server writing the responses so wire
	// ordering (response BEFORE notification) is observable.
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/tmp"}}` + "\n")
	var out syncBuffer
	srv := NewServer(in, &out, a.Handle)
	a.SetNotifier(srv.Notify)
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected response + notification, got %d lines: %s", len(lines), out.String())
	}
	if !strings.Contains(lines[0], "sessionId") {
		t.Errorf("first frame must be the session/new response: %s", lines[0])
	}
	if !strings.Contains(lines[1], "available_commands_update") || !strings.Contains(lines[1], `"name":"config"`) {
		t.Errorf("second frame must advertise commands: %s", lines[1])
	}
}

func TestACP_SetModeEmitsCurrentModeUpdate(t *testing.T) {
	be := &fakeBackend{}
	a := NewACP(be, "1.0.0")
	notes, nmu := notesRecorder(a)
	sid := acpNewSession(t, a)

	runLines(t, a.Handle, `{"jsonrpc":"2.0","id":2,"method":"session/set_mode","params":{"sessionId":"`+sid+`","modeId":"coder"}}`)
	nmu.Lock()
	joined := strings.Join(*notes, "\n")
	nmu.Unlock()
	if !strings.Contains(joined, "current_mode_update") || !strings.Contains(joined, `"currentModeId":"coder"`) {
		t.Errorf("set_mode must emit current_mode_update: %s", joined)
	}
}

func TestACP_PromptSlashSwitchesMode(t *testing.T) {
	be := &fakeBackend{}
	a := NewACP(be, "1.0.0")
	notes, nmu := notesRecorder(a)
	sid := acpNewSession(t, a)

	// Bare "/coder": switches the session and ends the turn.
	runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"/coder"}]}}`,
	)
	nmu.Lock()
	joined := strings.Join(*notes, "\n")
	nmu.Unlock()
	if !strings.Contains(joined, `"currentModeId":"coder"`) {
		t.Fatalf("slash mode switch must emit current_mode_update: %s", joined)
	}
	if be.lastTask != "" {
		t.Errorf("bare mode switch must not run a task, got %q", be.lastTask)
	}

	// "/coder run the tests": switches AND runs the task in the new mode.
	runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"/coder run the tests"}]}}`,
	)
	if be.lastTask != "run the tests" {
		t.Errorf("task after mode switch must run, got %q", be.lastTask)
	}
	// Session mode persisted: a later bare prompt drives CoderStream.
	runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":4,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"more work"}]}}`,
	)
	nmu.Lock()
	joined = strings.Join(*notes, "\n")
	nmu.Unlock()
	if !strings.Contains(joined, "coder done: more work") {
		t.Errorf("mode must persist on the session: %s", joined)
	}
}

func TestACP_PromptSlashRunsHeadlessCommand(t *testing.T) {
	be := &fakeBackend{cmdOut: "provider: openai\nmodel: gpt-x"}
	a := NewACP(be, "1.0.0")
	notes, nmu := notesRecorder(a)
	sid := acpNewSession(t, a)

	pr := runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"/config providers"}]}}`,
	)
	res, _ := json.Marshal(pr[0].Result)
	if !strings.Contains(string(res), "end_turn") {
		t.Errorf("slash command turn must end cleanly, got %s", res)
	}
	if be.lastCmd != "/config providers" {
		t.Errorf("full command line must reach the backend, got %q", be.lastCmd)
	}
	nmu.Lock()
	joined := strings.Join(*notes, "\n")
	nmu.Unlock()
	if !strings.Contains(joined, "provider: openai") {
		t.Errorf("command output must stream back: %s", joined)
	}
}

func TestACP_PromptSlashUnknownCommand(t *testing.T) {
	be := &fakeBackend{}
	a := NewACP(be, "1.0.0")
	notes, nmu := notesRecorder(a)
	sid := acpNewSession(t, a)

	runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"/gateway start"}]}}`,
	)
	if be.lastCmd != "" {
		t.Errorf("unknown command must not reach the backend, got %q", be.lastCmd)
	}
	if be.lastTask != "" {
		t.Errorf("unknown command must not leak to the LLM, got %q", be.lastTask)
	}
	nmu.Lock()
	joined := strings.Join(*notes, "\n")
	nmu.Unlock()
	if !strings.Contains(joined, "agent_message_chunk") {
		t.Errorf("unsupported command must answer with a message: %s", joined)
	}
}

// noCmdBackend implements ACPBackend WITHOUT the optional ACPCommandBackend
// capability — pins that the capability is truly optional.
type noCmdBackend struct{ f *fakeBackend }

func (b *noCmdBackend) Prompt(ctx context.Context, session, text string) (string, error) {
	return b.f.Prompt(ctx, session, text)
}
func (b *noCmdBackend) AgentStream(ctx context.Context, session, task string, opts RunOpts) (string, error) {
	return b.f.AgentStream(ctx, session, task, opts)
}
func (b *noCmdBackend) CoderStream(ctx context.Context, session, task string, opts RunOpts) (string, error) {
	return b.f.CoderStream(ctx, session, task, opts)
}

func TestACP_BackendWithoutCommandCapability(t *testing.T) {
	f := &fakeBackend{}
	a := NewACP(&noCmdBackend{f: f}, "1.0.0")
	notes, nmu := notesRecorder(a)
	sid := acpNewSession(t, a)

	nmu.Lock()
	joined := strings.Join(*notes, "\n")
	nmu.Unlock()
	if strings.Contains(joined, "available_commands_update") {
		t.Errorf("capability-less backend must not advertise commands: %s", joined)
	}

	// A known REPL command answers "unsupported" (never dispatched)…
	runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"/config providers"}]}}`,
	)
	if f.lastCmd != "" {
		t.Errorf("capability-less backend must never receive RunCommand, got %q", f.lastCmd)
	}
	// …and the agent loop itself still runs normally.
	runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"do work"}]}}`,
	)
	if f.lastTask != "do work" {
		t.Errorf("agent stream must work without the capability, got %q", f.lastTask)
	}
}

func TestACP_SlashTokenSplitsOnAnyWhitespace(t *testing.T) {
	// Editor prompt boxes produce "/coder\nfix it" — the token must still be
	// recognized as a mode switch and the task must run.
	be := &fakeBackend{}
	a := NewACP(be, "1.0.0")
	notesRecorder(a)
	sid := acpNewSession(t, a)

	runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"/coder\nfix the tests"}]}}`,
	)
	if be.lastTask != "fix the tests" {
		t.Errorf("newline-separated slash task must run, got %q", be.lastTask)
	}
}

func TestACP_UnknownSlashTokenFallsThroughToModel(t *testing.T) {
	// "/usr/local/bin explain this" is user text, not a command — it must
	// reach the model untouched (never hijack default input).
	be := &fakeBackend{}
	a := NewACP(be, "1.0.0")
	notesRecorder(a)
	sid := acpNewSession(t, a)

	runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"/usr/local/bin explain this"}]}}`,
	)
	if be.lastTask != "/usr/local/bin explain this" {
		t.Errorf("unknown slash token must reach the model, got %q", be.lastTask)
	}
	if be.lastCmd != "" {
		t.Errorf("unknown token must not run as a command, got %q", be.lastCmd)
	}
}

func TestACP_PromptFlattensResourceBlocks(t *testing.T) {
	be := &fakeBackend{}
	a := NewACP(be, "1.0.0")
	notesRecorder(a)
	sid := acpNewSession(t, a)

	runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[`+
			`{"type":"text","text":"review this"},`+
			`{"type":"resource_link","uri":"file:///tmp/a.go","name":"a.go"},`+
			`{"type":"resource","resource":{"uri":"file:///tmp/b.go","text":"package b"}}]}}`,
	)
	task := be.lastTask
	if !strings.Contains(task, "review this") {
		t.Errorf("text part missing: %q", task)
	}
	if !strings.Contains(task, "/tmp/a.go") {
		t.Errorf("resource_link must surface the file reference: %q", task)
	}
	if !strings.Contains(task, "package b") || !strings.Contains(task, "/tmp/b.go") {
		t.Errorf("embedded resource must surface its contents: %q", task)
	}
	// Parts must not be glued together without separators.
	if strings.Contains(task, "review thisReferenced") {
		t.Errorf("parts glued without separator: %q", task)
	}
}

func TestACP_OverlappingPromptRejected(t *testing.T) {
	be := &fakeBackend{blockAgent: make(chan struct{})}
	a := NewACP(be, "1.0.0")
	notesRecorder(a)
	sid := acpAgentSession(t, a)

	done := make(chan []Response, 1)
	go func() {
		done <- runLines(t, a.Handle,
			`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"first"}]}}`,
		)
	}()
	time.Sleep(100 * time.Millisecond)

	second := runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"second"}]}}`,
	)
	if second[0].Error == nil {
		t.Error("overlapping prompt on the same session must be rejected")
	}

	close(be.blockAgent)
	select {
	case resps := <-done:
		res, _ := json.Marshal(resps[0].Result)
		if !strings.Contains(string(res), "end_turn") {
			t.Errorf("first prompt must finish normally, got %s", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first prompt never finished")
	}
	// After the first prompt completes, the session accepts prompts again.
	third := runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":4,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"third"}]}}`,
	)
	if third[0].Error != nil {
		t.Errorf("session must accept prompts after the previous one ends: %+v", third[0].Error)
	}
}

func TestACP_CancelMarksOpenToolCallsFailed(t *testing.T) {
	be := &fakeBackend{blockAgent: make(chan struct{})}
	a := NewACP(be, "1.0.0")
	notes, nmu := notesRecorder(a)
	sid := acpAgentSession(t, a)

	done := make(chan []Response, 1)
	go func() {
		done <- runLines(t, a.Handle,
			`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"never ends"}]}}`,
		)
	}()
	time.Sleep(100 * time.Millisecond)
	runLines(t, a.Handle, `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"`+sid+`"}}`)

	select {
	case resps := <-done:
		res, _ := json.Marshal(resps[0].Result)
		if !strings.Contains(string(res), "cancelled") {
			t.Errorf("expected cancelled stopReason, got %s", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel must interrupt the in-flight prompt")
	}
	nmu.Lock()
	joined := strings.Join(*notes, "\n")
	nmu.Unlock()
	// The tool_call opened before the block must be closed as failed.
	if !strings.Contains(joined, `"toolCallId":"call-1"`) || !strings.Contains(joined, `"status":"failed"`) {
		t.Errorf("open tool calls must be failed on cancel: %s", joined)
	}
}

func TestACP_RequestPermissionAllowAndReject(t *testing.T) {
	for _, tt := range []struct {
		name    string
		outcome string
		granted bool
	}{
		{"allow", `{"outcome":{"outcome":"selected","optionId":"allow-once"}}`, true},
		{"reject", `{"outcome":{"outcome":"selected","optionId":"reject-once"}}`, false},
		{"cancelled", `{"outcome":{"outcome":"cancelled"}}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			be := &fakeBackend{askDanger: true}
			a := NewACP(be, "1.0.0")
			notesRecorder(a)
			var gotMethod string
			a.SetRequester(func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
				gotMethod = method
				b, _ := json.Marshal(params)
				if !strings.Contains(string(b), "toolCall") || !strings.Contains(string(b), "allow-once") {
					t.Errorf("permission params incomplete: %s", b)
				}
				return json.RawMessage(tt.outcome), nil
			})
			sid := acpAgentSession(t, a)
			runLines(t, a.Handle,
				`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"danger"}]}}`,
			)
			if gotMethod != "session/request_permission" {
				t.Fatalf("expected a permission request, got %q", gotMethod)
			}
			if be.permGranted != tt.granted {
				t.Errorf("granted = %v, want %v", be.permGranted, tt.granted)
			}
		})
	}
}

func TestACP_SetModeChat(t *testing.T) {
	be := &fakeBackend{reply: "the answer"}
	a := NewACP(be, "1.0.0")
	notes, nmu := notesRecorder(a)
	sid := acpNewSession(t, a)

	r := runLines(t, a.Handle, `{"jsonrpc":"2.0","id":2,"method":"session/set_mode","params":{"sessionId":"`+sid+`","modeId":"chat"}}`)
	if r[0].Error != nil {
		t.Fatalf("set_mode failed: %+v", r[0].Error)
	}
	runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"q"}]}}`,
	)
	nmu.Lock()
	joined := strings.Join(*notes, "\n")
	nmu.Unlock()
	if !strings.Contains(joined, "the answer") {
		t.Errorf("chat mode must stream the reply, got: %s", joined)
	}
	// Unknown mode is rejected.
	r = runLines(t, a.Handle, `{"jsonrpc":"2.0","id":4,"method":"session/set_mode","params":{"sessionId":"`+sid+`","modeId":"nope"}}`)
	if r[0].Error == nil {
		t.Error("unknown mode must error")
	}
}

func TestACP_CancelInterruptsInFlightPrompt(t *testing.T) {
	be := &fakeBackend{blockAgent: make(chan struct{})}
	a := NewACP(be, "1.0.0")
	notesRecorder(a)
	sid := acpAgentSession(t, a)

	done := make(chan []Response, 1)
	go func() {
		done <- runLines(t, a.Handle,
			`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"never ends"}]}}`,
		)
	}()

	// Give the prompt time to start blocking, then cancel it.
	time.Sleep(100 * time.Millisecond)
	runLines(t, a.Handle, `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"`+sid+`"}}`)

	select {
	case resps := <-done:
		res, _ := json.Marshal(resps[0].Result)
		if !strings.Contains(string(res), "cancelled") {
			t.Errorf("expected cancelled stopReason, got %s", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel must interrupt the in-flight prompt")
	}
}

func TestACP_UnknownSessionRejected(t *testing.T) {
	a := NewACP(&fakeBackend{}, "1.0.0")
	r := runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"ghost","prompt":[{"type":"text","text":"q"}]}}`,
	)
	if r[0].Error == nil {
		t.Error("unknown session must be rejected")
	}
}

func TestACP_Initialize(t *testing.T) {
	a := NewACP(&fakeBackend{}, "1.0.0")
	resps := runLines(t, a.Handle, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	body, _ := json.Marshal(resps[0].Result)
	if !strings.Contains(string(body), "agentCapabilities") {
		t.Errorf("initialize result wrong: %s", body)
	}
	if !strings.Contains(string(body), `"agentInfo"`) || !strings.Contains(string(body), `"chatcli"`) {
		t.Errorf("initialize must carry agentInfo: %s", body)
	}
}
