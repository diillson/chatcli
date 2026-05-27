package rpcserve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// fakeBackend echoes prompts, recording the session.
type fakeBackend struct {
	lastSession string
	reply       string
	err         error
	lastTask    string
	lastTool    string
	lastArgs    string
}

func (f *fakeBackend) Prompt(_ context.Context, session, text string) (string, error) {
	f.lastSession = session
	if f.err != nil {
		return "", f.err
	}
	if f.reply != "" {
		return f.reply, nil
	}
	return "echo:" + text, nil
}

func (f *fakeBackend) Agent(_ context.Context, session, task string) (string, error) {
	f.lastSession, f.lastTask = session, task
	return "agent-ran:" + task, nil
}

func (f *fakeBackend) Coder(_ context.Context, session, task string) (string, error) {
	f.lastSession, f.lastTask = session, task
	return "coder-ran:" + task, nil
}

func (f *fakeBackend) BuiltinTools() []ToolInfo {
	return []ToolInfo{{Name: "read", Description: "Read a file"}, {Name: "search", Description: "Search files"}}
}

func (f *fakeBackend) CallBuiltin(_ context.Context, name, args string) (string, error) {
	f.lastTool, f.lastArgs = name, args
	return "tool:" + name + ":" + args, nil
}

// runOne feeds a single request line through a Server with the given handler
// and returns the decoded response lines.
func runLines(t *testing.T, handler handlerFunc, lines ...string) []Response {
	t.Helper()
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out strings.Builder
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
		resps = append(resps, r)
	}
	return resps
}

func TestJSONRPC_NotificationGetsNoResponse(t *testing.T) {
	h := func(context.Context, string, json.RawMessage) (interface{}, *RPCError) { return "ok", nil }
	// A request without id is a notification.
	resps := runLines(t, h, `{"jsonrpc":"2.0","method":"foo"}`)
	if len(resps) != 0 {
		t.Errorf("notification should produce no response, got %d", len(resps))
	}
}

func TestJSONRPC_ParseError(t *testing.T) {
	h := func(context.Context, string, json.RawMessage) (interface{}, *RPCError) { return nil, nil }
	resps := runLines(t, h, `{not json`)
	if len(resps) != 1 || resps[0].Error == nil || resps[0].Error.Code != CodeParseError {
		t.Fatalf("expected parse error, got %+v", resps)
	}
}

func TestMCP_InitializeAndToolsList(t *testing.T) {
	m := NewMCP(&fakeBackend{}, "chatcli", "1.0.0")
	resps := runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	)
	if len(resps) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(resps))
	}
	init, _ := json.Marshal(resps[0].Result)
	if !strings.Contains(string(init), MCPProtocolVersion) || !strings.Contains(string(init), "chatcli") {
		t.Errorf("initialize result wrong: %s", init)
	}
	list, _ := json.Marshal(resps[1].Result)
	if !strings.Contains(string(list), "ask_chatcli") {
		t.Errorf("tools/list missing ask_chatcli: %s", list)
	}
}

func TestMCP_ToolCall(t *testing.T) {
	be := &fakeBackend{}
	m := NewMCP(be, "chatcli", "1.0.0")
	resps := runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_chatcli","arguments":{"prompt":"hi","session":"s1"}}}`,
	)
	if len(resps) != 1 || resps[0].Error != nil {
		t.Fatalf("unexpected: %+v", resps)
	}
	if be.lastSession != "s1" {
		t.Errorf("session not propagated: %q", be.lastSession)
	}
	body, _ := json.Marshal(resps[0].Result)
	if !strings.Contains(string(body), "echo:hi") {
		t.Errorf("tool result wrong: %s", body)
	}
}

func TestMCP_ToolCall_MissingPrompt(t *testing.T) {
	m := NewMCP(&fakeBackend{}, "chatcli", "1.0.0")
	resps := runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_chatcli","arguments":{}}}`,
	)
	if len(resps) != 1 || resps[0].Error == nil || resps[0].Error.Code != CodeInvalidParams {
		t.Fatalf("expected invalid params, got %+v", resps)
	}
}

func TestMCP_AdvertisesAgentCoderAndBuiltins(t *testing.T) {
	m := NewMCP(&fakeBackend{}, "chatcli", "1.0.0")
	resps := runLines(t, m.Handle, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	body, _ := json.Marshal(resps[0].Result)
	for _, want := range []string{"ask_chatcli", "agent_task", "coder_task", "read", "search"} {
		if !strings.Contains(string(body), `"`+want+`"`) {
			t.Errorf("tools/list missing %q: %s", want, body)
		}
	}
}

func TestMCP_AgentCoderBuiltinDispatch(t *testing.T) {
	be := &fakeBackend{}
	m := NewMCP(be, "chatcli", "1.0.0")

	// agent_task
	r := runLines(t, m.Handle, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"agent_task","arguments":{"task":"build X"}}}`)
	if b, _ := json.Marshal(r[0].Result); !strings.Contains(string(b), "agent-ran:build X") {
		t.Errorf("agent_task wrong: %s", b)
	}
	// coder_task
	r = runLines(t, m.Handle, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"coder_task","arguments":{"task":"fix Y"}}}`)
	if b, _ := json.Marshal(r[0].Result); !strings.Contains(string(b), "coder-ran:fix Y") {
		t.Errorf("coder_task wrong: %s", b)
	}
	// builtin tool
	r = runLines(t, m.Handle, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read","arguments":{"args":"main.go"}}}`)
	if b, _ := json.Marshal(r[0].Result); !strings.Contains(string(b), "tool:read:main.go") {
		t.Errorf("builtin dispatch wrong: %s", b)
	}
	if be.lastTool != "read" || be.lastArgs != "main.go" {
		t.Errorf("builtin args not propagated: %q %q", be.lastTool, be.lastArgs)
	}
	// missing task -> error
	r = runLines(t, m.Handle, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"agent_task","arguments":{}}}`)
	if r[0].Error == nil {
		t.Error("expected error for agent_task without task")
	}
}

func TestACP_NewAndPrompt(t *testing.T) {
	be := &fakeBackend{reply: "the answer"}
	a := NewACP(be, "1.0.0")

	// Capture notifications by wiring a notifier that records them.
	var notes []string
	a.SetNotifier(func(method string, params interface{}) error {
		b, _ := json.Marshal(params)
		notes = append(notes, method+":"+string(b))
		return nil
	})

	// session/new
	newResps := runLines(t, a.Handle, `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/tmp"}}`)
	body, _ := json.Marshal(newResps[0].Result)
	var nr struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(body, &nr)
	if nr.SessionID == "" {
		t.Fatal("session/new should return a sessionId")
	}

	// session/prompt
	promptReq := `{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"` + nr.SessionID +
		`","prompt":[{"type":"text","text":"question"}]}}`
	pr := runLines(t, a.Handle, promptReq)
	res, _ := json.Marshal(pr[0].Result)
	if !strings.Contains(string(res), "end_turn") {
		t.Errorf("expected end_turn stopReason, got %s", res)
	}
	// The answer must have been streamed as a session/update.
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "session/update") || !strings.Contains(joined, "the answer") {
		t.Errorf("expected streamed agent message chunk, got: %s", joined)
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
