/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/rpcserve"
	"github.com/diillson/chatcli/models"
)

// proposingModel answers a chat turn the way a model does: told how to
// hand work to coder mode it proposes, otherwise it sends the user to
// /coder.
func proposingModel(system string) string {
	if strings.Contains(system, "<coder_handoff>") {
		return "That needs the workspace.\n\n<coder_handoff>list the files here</coder_handoff>"
	}
	return "Use /coder for that."
}

// rawEvents posts one turn and returns every event of its stream, untyped.
func (w *e2eWeb) rawEvents(t *testing.T, mode, text string) []map[string]interface{} {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"session": "web", "mode": mode, "text": text})
	req, _ := http.NewRequest(http.MethodPost, "http://"+w.srv.Host()+"/api/turn", bytes.NewReader(body))
	req.Header.Set("X-Web-Token", w.srv.Token())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("turn %q: status %d", text, resp.StatusCode)
	}
	var out []map[string]interface{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var ev map[string]interface{}
		if line := sc.Text(); strings.HasPrefix(line, "data: ") && json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) == nil {
			out = append(out, ev)
		}
	}
	return out
}

// The web page, over the real backend and engine: a chat turn that needs
// the workspace comes back with the reply clean and the task as a handoff
// event; accepting it is a coder turn with that task, in the same session.
func TestWebE2E_ChatProposesTheCoderHandoff(t *testing.T) {
	w := startE2EWeb(t)
	w.model.chat = proposingModel

	var task, reply, streamed string
	for _, ev := range w.rawEvents(t, "chat", "which files are here?") {
		switch ev["type"] {
		case "chunk":
			s, _ := ev["text"].(string)
			streamed += s
		case "handoff":
			task, _ = ev["text"].(string)
		case "done":
			reply, _ = ev["reply"].(string)
		}
	}
	if task != "list the files here" {
		t.Fatalf("handoff task = %q", task)
	}
	if reply != "That needs the workspace." || strings.Contains(streamed, "coder_handoff") {
		t.Fatalf("the proposal line reached the page: reply=%q streamed=%q", reply, streamed)
	}

	// The page accepts: the task runs as a coder turn and reaches the model.
	w.rawEvents(t, "chat", "/coder "+task)
	if len(w.model.callsWith("list the files here")) == 0 {
		t.Fatal("the accepted task never reached the coder")
	}
}

// The same backend behind MCP and ACP: a chat call that says it can
// confirm gets the task back, and one that cannot keeps the old answer.
func TestRPCBackend_PromptWithCarriesTheHandoff(t *testing.T) {
	w := startE2EWeb(t)
	w.model.chat = proposingModel
	b := &rpcBackend{mgr: &e2eManager{model: w.model}, cli: w.engine, store: w.engine, provider: "OPENAI", model: "gpt-4o", sessions: map[string][]models.Message{}}

	var task string
	reply, err := b.PromptWith(context.Background(), "mcp", "which files are here?", rpcserve.RunOpts{HandoffOut: &task})
	if err != nil || task != "list the files here" || reply != "That needs the workspace." {
		t.Fatalf("confirming surface: reply=%q task=%q err=%v", reply, task, err)
	}

	reply, err = b.Prompt(context.Background(), "gateway", "which files are here?")
	if err != nil || reply != "Use /coder for that." {
		t.Fatalf("surface that cannot confirm: reply=%q err=%v", reply, err)
	}
}
