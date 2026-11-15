/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package rpcserve

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// acpChatSession opens a session and puts it in chat mode.
func acpChatSession(t *testing.T, a *ACP) string {
	t.Helper()
	sid := acpNewSession(t, a)
	if r := runLines(t, a.Handle, `{"jsonrpc":"2.0","id":90,"method":"session/set_mode","params":{"sessionId":"`+sid+`","modeId":"chat"}}`); r[0].Error != nil {
		t.Fatalf("set_mode chat: %+v", r[0].Error)
	}
	return sid
}

func acpPrompt(t *testing.T, a *ACP, sid, text string) string {
	t.Helper()
	r := runLines(t, a.Handle, `{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"`+text+`"}]}}`)
	if r[0].Error != nil {
		t.Fatalf("prompt: %+v", r[0].Error)
	}
	b, _ := json.Marshal(r[0].Result)
	return string(b)
}

// answerPermission wires a client that answers session/request_permission
// with one option and records what it was asked.
func answerPermission(a *ACP, optionID string) *[]string {
	var mu sync.Mutex
	asked := &[]string{}
	a.SetRequester(func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		b, _ := json.Marshal(params)
		mu.Lock()
		*asked = append(*asked, method+":"+string(b))
		mu.Unlock()
		return json.RawMessage(`{"outcome":{"outcome":"selected","optionId":"` + optionID + `"}}`), nil
	})
	return asked
}

func joined(notes *[]string, mu *sync.Mutex) string {
	mu.Lock()
	defer mu.Unlock()
	return strings.Join(*notes, "\n")
}

// Accepted: the chat reply is delivered, the client is asked once with the
// task in the dialog, the session moves to coder mode and the coder run
// happens in the same turn.
func TestACPChat_HandoffAcceptedRunsTheCoderInTheSameTurn(t *testing.T) {
	be := &fakeBackend{reply: "The tests need a run.", handoffTask: "run the tests"}
	a := NewACP(be, "1.0.0")
	notes, nmu := notesRecorder(a)
	asked := answerPermission(a, "allow-once")
	sid := acpChatSession(t, a)

	if res := acpPrompt(t, a, sid, "do the tests pass?"); !strings.Contains(res, "end_turn") {
		t.Fatalf("stop reason: %s", res)
	}
	if len(*asked) != 1 || !strings.Contains((*asked)[0], "session/request_permission") || !strings.Contains((*asked)[0], "run the tests") {
		t.Fatalf("permission dialog: %v", *asked)
	}
	if strings.Contains((*asked)[0], "allow-always") {
		t.Fatalf("a handoff is confirmed one at a time: %v", *asked)
	}
	all := joined(notes, nmu)
	for _, want := range []string{"The tests need a run.", `"currentModeId":"coder"`, "coder done: run the tests"} {
		if !strings.Contains(all, want) {
			t.Errorf("updates lack %q:\n%s", want, all)
		}
	}
	if strings.Index(all, "The tests need a run.") > strings.Index(all, "coder done: run the tests") {
		t.Error("the chat reply must come before the coder run")
	}
	be.mu.Lock()
	task := be.lastTask
	be.mu.Unlock()
	if task != "run the tests" {
		t.Fatalf("coder ran %q", task)
	}
	// The session stays in coder mode, like a typed "/coder <task>".
	be.handoffTask = ""
	acpPrompt(t, a, sid, "more work")
	if be.lastTask != "more work" {
		t.Fatalf("the next prompt did not go to the coder: %q", be.lastTask)
	}
}

// Declined: nothing runs, the session stays in chat and the dialog's tool
// call is closed so the client keeps no pending entry.
func TestACPChat_HandoffDeclinedStaysInChat(t *testing.T) {
	be := &fakeBackend{reply: "The tests need a run.", handoffTask: "run the tests"}
	a := NewACP(be, "1.0.0")
	notes, nmu := notesRecorder(a)
	answerPermission(a, "reject-once")
	sid := acpChatSession(t, a)

	acpPrompt(t, a, sid, "do the tests pass?")
	all := joined(notes, nmu)
	if be.lastTask != "" || strings.Contains(all, `"currentModeId":"coder"`) {
		t.Fatalf("a declined handoff ran or switched mode: task=%q\n%s", be.lastTask, all)
	}
	if !strings.Contains(all, `"status":"failed"`) {
		t.Errorf("the declined proposal was left pending:\n%s", all)
	}
}

// A client with no permission dialog cannot confirm: nothing runs and the
// reply says how to start the task by hand.
func TestACPChat_HandoffWithoutADialogGivesTheCommand(t *testing.T) {
	for name, wire := range map[string]func(a *ACP){
		"no requester": func(*ACP) {},
		"method not found": func(a *ACP) {
			a.SetRequester(func(context.Context, string, interface{}) (json.RawMessage, error) {
				return nil, errf(CodeMethodNotFound, "no such method")
			})
		},
	} {
		be := &fakeBackend{reply: "The tests need a run.", handoffTask: "run the tests"}
		a := NewACP(be, "1.0.0")
		notes, nmu := notesRecorder(a)
		wire(a)
		sid := acpChatSession(t, a)
		acpPrompt(t, a, sid, "do the tests pass?")
		all := joined(notes, nmu)
		if be.lastTask != "" {
			t.Fatalf("%s: the coder ran without a confirmation", name)
		}
		// The catalog is not loaded in this package's tests, so the
		// message shows as its key followed by the task.
		if !strings.Contains(all, "acp.handoff.hint run the tests") {
			t.Errorf("%s: no way to start it by hand:\n%s", name, all)
		}
	}
}

// A chat reply with no proposal asks nothing.
func TestACPChat_NoProposalNoDialog(t *testing.T) {
	be := &fakeBackend{reply: "Hello."}
	a := NewACP(be, "1.0.0")
	asked := answerPermission(a, "allow-once")
	sid := acpChatSession(t, a)
	acpPrompt(t, a, sid, "hi")
	if len(*asked) != 0 || be.lastTask != "" {
		t.Fatalf("asked=%v task=%q", *asked, be.lastTask)
	}
	if be.lastOpts.HandoffOut == nil {
		t.Fatal("the ACP chat turn did not say it can confirm a handoff")
	}
}

func mcpAsk(t *testing.T, m *MCP, args map[string]interface{}) (string, bool) {
	t.Helper()
	params, _ := json.Marshal(map[string]interface{}{"name": "ask_chatcli", "arguments": args})
	res, rpcErr := m.callTool(context.Background(), params)
	if rpcErr != nil {
		t.Fatalf("ask_chatcli: %+v", rpcErr)
	}
	b, _ := json.Marshal(res)
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	_ = json.Unmarshal(b, &out)
	if len(out.Content) == 0 {
		t.Fatalf("empty result: %s", b)
	}
	return out.Content[0].Text, out.IsError
}

// elicitingMCP is an MCP server whose client answers elicitation.
func elicitingMCP(be *fakeBackend, answer string) (*MCP, *[]string) {
	m := NewMCP(be, "chatcli", "test")
	asked := &[]string{}
	m.SetRequester(func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		b, _ := json.Marshal(params)
		*asked = append(*asked, method+":"+string(b))
		return json.RawMessage(answer), nil
	})
	m.initialize(json.RawMessage(`{"capabilities":{"elicitation":{}}}`))
	return m, asked
}

func askReadOnly(t *testing.T, m *MCP) (bool, string) {
	t.Helper()
	for _, d := range m.toolDefinitions() {
		if d["name"] == "ask_chatcli" {
			ro, _ := d["annotations"].(map[string]interface{})["readOnlyHint"].(bool)
			desc, _ := d["description"].(string)
			return ro, desc
		}
	}
	t.Fatal("ask_chatcli is not advertised")
	return false, ""
}

// With elicitation the user is asked and, on approval, the coder run
// happens in the same call: the result is the chat reply followed by the
// run. The tool is then not advertised as read-only.
func TestMCPAsk_HandoffApprovedRunsTheCoderInTheSameCall(t *testing.T) {
	be := &fakeBackend{reply: "The tests need a run.", handoffTask: "run the tests"}
	m, asked := elicitingMCP(be, `{"action":"accept","content":{"approve":true}}`)

	text, isErr := mcpAsk(t, m, map[string]interface{}{"prompt": "do the tests pass?", "session": "s1"})
	if isErr || !strings.HasPrefix(text, "The tests need a run.") || !strings.Contains(text, "coder-ran:run the tests") {
		t.Fatalf("result: %q", text)
	}
	if len(*asked) != 1 || !strings.Contains((*asked)[0], "elicitation/create") || !strings.Contains((*asked)[0], "run the tests") {
		t.Fatalf("elicitation: %v", *asked)
	}
	if be.lastSession != "s1" || be.lastOpts.HandoffOut != nil || be.lastOpts.Permissions == nil {
		t.Fatalf("coder run: session=%q opts=%+v", be.lastSession, be.lastOpts)
	}
	if ro, desc := askReadOnly(t, m); ro || !strings.Contains(desc, "same call") {
		t.Fatalf("ask_chatcli can start a coder run here: readOnly=%v desc=%q", ro, desc)
	}
}

// A refusal runs nothing and tells the calling model not to start it.
func TestMCPAsk_HandoffDeclinedRunsNothing(t *testing.T) {
	for name, answer := range map[string]string{
		"unchecked": `{"action":"accept","content":{"approve":false}}`,
		"declined":  `{"action":"decline"}`,
	} {
		be := &fakeBackend{reply: "The tests need a run.", handoffTask: "run the tests"}
		m, _ := elicitingMCP(be, answer)
		text, _ := mcpAsk(t, m, map[string]interface{}{"prompt": "do the tests pass?"})
		if be.lastTask != "" {
			t.Fatalf("%s: the coder ran after a refusal", name)
		}
		if !strings.Contains(text, "declined") || strings.Contains(text, "call the coder_task tool") {
			t.Fatalf("%s: %q", name, text)
		}
	}
}

// Without elicitation nobody can be asked: nothing runs, the reply ends
// with the task for the caller to pass to coder_task, and the tool stays
// read-only.
func TestMCPAsk_HandoffWithoutElicitationPointsAtCoderTask(t *testing.T) {
	be := &fakeBackend{reply: "The tests need a run.", handoffTask: "run the tests"}
	m := NewMCP(be, "chatcli", "test")
	m.initialize(json.RawMessage(`{"capabilities":{}}`))

	text, isErr := mcpAsk(t, m, map[string]interface{}{"prompt": "do the tests pass?"})
	if isErr || be.lastTask != "" {
		t.Fatalf("ran=%q err=%v", be.lastTask, isErr)
	}
	if !strings.HasPrefix(text, "The tests need a run.") || !strings.Contains(text, "coder_task") || !strings.HasSuffix(text, "run the tests") {
		t.Fatalf("result: %q", text)
	}
	if ro, desc := askReadOnly(t, m); !ro || !strings.Contains(desc, "[coder handoff]") {
		t.Fatalf("readOnly=%v desc=%q", ro, desc)
	}
}

// The elicitation kill switch and the handoff switch both keep ask_chatcli
// read-only, and a plain call never asks for a proposal.
func TestMCPAsk_HandoffSwitches(t *testing.T) {
	be := &fakeBackend{reply: "ok", handoffTask: "run the tests"}
	m, asked := elicitingMCP(be, `{"action":"accept","content":{"approve":true}}`)

	if text, _ := mcpAsk(t, m, map[string]interface{}{"prompt": "raw", "plain": true}); text != "ok" || be.lastOpts.HandoffOut != nil {
		t.Fatalf("plain call: %q opts=%+v", text, be.lastOpts)
	}

	t.Setenv("CHATCLI_MCP_ELICITATION", "off")
	if ro, _ := askReadOnly(t, m); !ro {
		t.Fatal("no elicitation: nothing can run, the tool is read-only")
	}
	if text, _ := mcpAsk(t, m, map[string]interface{}{"prompt": "x"}); !strings.Contains(text, "coder_task") || be.lastTask != "" {
		t.Fatalf("kill switch: %q task=%q", text, be.lastTask)
	}
	t.Setenv("CHATCLI_MCP_ELICITATION", "")

	t.Setenv("CHATCLI_CHAT_CODER_HANDOFF", "false")
	if ro, desc := askReadOnly(t, m); !ro || strings.Contains(desc, "coder") {
		t.Fatalf("handoff off: readOnly=%v desc=%q", ro, desc)
	}
	if len(*asked) != 0 {
		t.Fatalf("asked: %v", *asked)
	}
}

// A user who was asked and gave no answer did not say yes: nothing runs
// and the calling model is not pointed at coder_task either.
func TestMCPAsk_HandoffUnansweredIsNotAYes(t *testing.T) {
	t.Setenv("CHATCLI_MCP_PERMISSION_TIMEOUT", "20ms")
	be := &fakeBackend{reply: "The tests need a run.", handoffTask: "run the tests"}
	m := NewMCP(be, "chatcli", "test")
	m.SetRequester(func(ctx context.Context, _ string, _ interface{}) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	m.initialize(json.RawMessage(`{"capabilities":{"elicitation":{}}}`))
	text, isErr := mcpAsk(t, m, map[string]interface{}{"prompt": "do the tests pass?"})
	if isErr || be.lastTask != "" {
		t.Fatalf("ran=%q err=%v", be.lastTask, isErr)
	}
	if !strings.Contains(text, "did not answer") || strings.Contains(text, "call the coder_task tool") {
		t.Fatalf("result: %q", text)
	}
}

// An approved run that fails keeps the reply the approval was based on.
func TestMCPAsk_HandoffRunFailureKeepsTheReply(t *testing.T) {
	be := &failingCoder{fakeBackend: &fakeBackend{reply: "The tests need a run.", handoffTask: "run the tests"}}
	m := NewMCP(be, "chatcli", "test")
	m.SetRequester(func(context.Context, string, interface{}) (json.RawMessage, error) {
		return json.RawMessage(`{"action":"accept","content":{"approve":true}}`), nil
	})
	m.initialize(json.RawMessage(`{"capabilities":{"elicitation":{}}}`))
	text, isErr := mcpAsk(t, m, map[string]interface{}{"prompt": "do the tests pass?"})
	if !isErr || !strings.HasPrefix(text, "The tests need a run.") || !strings.Contains(text, "error: boom") {
		t.Fatalf("isError=%v text=%q", isErr, text)
	}
}

type failingCoder struct{ *fakeBackend }

func (f *failingCoder) Coder(context.Context, string, string, RunOpts) (string, error) {
	return "", errStr("boom")
}

// A client that declared elicitation but has no dialog is a client that
// does not elicit: the reply points at coder_task.
func TestMCPAsk_HandoffUnsupportedDialogFallsBack(t *testing.T) {
	be := &fakeBackend{reply: "The tests need a run.", handoffTask: "run the tests"}
	m := NewMCP(be, "chatcli", "test")
	m.SetRequester(func(context.Context, string, interface{}) (json.RawMessage, error) {
		return nil, errf(CodeMethodNotFound, "no such method")
	})
	m.initialize(json.RawMessage(`{"capabilities":{"elicitation":{}}}`))
	text, isErr := mcpAsk(t, m, map[string]interface{}{"prompt": "do the tests pass?"})
	if isErr || be.lastTask != "" || !strings.Contains(text, "coder_task") {
		t.Fatalf("ran=%q err=%v text=%q", be.lastTask, isErr, text)
	}
}
