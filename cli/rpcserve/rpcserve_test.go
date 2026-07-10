package rpcserve

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackend echoes prompts and records the last call details.
type fakeBackend struct {
	mu          sync.Mutex
	lastSession string
	reply       string
	err         error
	lastTask    string
	lastTool    string
	lastArgs    string
	lastOpts    RunOpts
	blockAgent  chan struct{} // when non-nil, AgentStream blocks until ctx cancel or close
}

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
	if opts.Emit != nil {
		opts.Emit("coder-line")
	}
	return "coder-ran:" + task, nil
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
	return nr.SessionID
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

func TestACP_AgentModeStreamsChunks(t *testing.T) {
	be := &fakeBackend{}
	a := NewACP(be, "1.0.0")
	notes, nmu := notesRecorder(a)
	sid := acpNewSession(t, a)

	pr := runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"build it"}]}}`,
	)
	res, _ := json.Marshal(pr[0].Result)
	if !strings.Contains(string(res), "end_turn") {
		t.Errorf("expected end_turn, got %s", res)
	}
	nmu.Lock()
	joined := strings.Join(*notes, "\n")
	nmu.Unlock()
	if !strings.Contains(joined, "working on: build it") || !strings.Contains(joined, "agent_message_chunk") {
		t.Errorf("agent progress must stream as chunks, got: %s", joined)
	}
	if be.lastTask != "build it" {
		t.Errorf("task not propagated: %q", be.lastTask)
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
	sid := acpNewSession(t, a)

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
}
