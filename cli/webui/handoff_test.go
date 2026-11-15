/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agentevents"
)

// A chat reply that proposes a coder handoff reaches the page as a
// "handoff" event ahead of the final one: the page shows the proposal and
// runs nothing by itself.
func TestChatTurn_ProposesTheHandoffToThePage(t *testing.T) {
	srv, fb := startTest(t)
	fb.handoff = "run the tests"
	code, evs, body := postTurn(t, srv, "chat", "do the tests pass?")
	if code != http.StatusOK || types(evs) != "run,chunk,chunk,handoff,done" {
		t.Fatalf("%d %s %s", code, types(evs), body)
	}
	for _, ev := range evs {
		if ev.Type == "handoff" && ev.Text != "run the tests" {
			t.Fatalf("handoff carries %q", ev.Text)
		}
	}
	fb.mu.Lock()
	defer fb.mu.Unlock()
	if fb.coderRuns != 0 {
		t.Fatal("the proposal started a coder run without the user's answer")
	}
	if len(fb.seen) != 1 || fb.seen[0].HandoffOut == nil {
		t.Fatal("the chat turn did not tell the backend the page can confirm")
	}
}

// A reply with no proposal sends no handoff event.
func TestChatTurn_NoProposalNoEvent(t *testing.T) {
	srv, _ := startTest(t)
	code, evs, body := postTurn(t, srv, "chat", "hi")
	if code != http.StatusOK || types(evs) != "run,chunk,chunk,done" {
		t.Fatalf("%d %s %s", code, types(evs), body)
	}
}

// The page has the card, its strings and the accept path that switches the
// mode selector to coder before sending the task.
func TestPage_HandoffCard(t *testing.T) {
	page := string(Page())
	for _, want := range []string{
		"case 'handoff': state.handoff = ev.text",
		"function offerHandoff(task)",
		"sendTurn('/coder ' + task)",
		"if (!(await sent)) offerHandoff(task)",
		"(replace || s.text.trim() === '')",
		"'handoff.title'", "'handoff.accept'", "'handoff.dismiss'", "'handoff.dismissed'",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

// Accepting sends "/coder <task>": whatever the task text starts with, it
// runs as the coder's task and is never read as a command of its own.
func TestAcceptedHandoff_TaskIsNeverACommand(t *testing.T) {
	srv, fb := startPlainCoder(t)
	code, evs, body := postTurn(t, srv, "chat", "/coder /agent wipe the build")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var reply string
	for _, ev := range evs {
		if ev.Type == "done" {
			reply = ev.Reply
		}
	}
	if fb.tasks != "/agent wipe the build" || reply != "done: /agent wipe the build" {
		t.Fatalf("coder tasks=%q reply=%q events=%s", fb.tasks, reply, types(evs))
	}
}

// A chat turn's final reply replaces the streamed text on the page; a loop
// turn's does not, its stream being the transcript.
func TestDone_ChatReplyReplacesTheStream(t *testing.T) {
	srv, _ := startPlainCoder(t)
	_, evs, _ := postTurn(t, srv, "chat", "hi")
	if last := evs[len(evs)-1]; last.Type != "done" || !last.Replace {
		t.Fatalf("chat done: %+v", last)
	}
	_, evs, _ = postTurn(t, srv, "coder", "build")
	if last := evs[len(evs)-1]; last.Type != "done" || last.Replace {
		t.Fatalf("coder done: %+v", last)
	}
}

// plainCoder is the fake backend with a coder that answers at once, so a
// test can post a coder turn without a permission dialog to answer.
type plainCoder struct {
	*fakeBackend
	tasks string
}

func (p *plainCoder) RunCoder(_ context.Context, _, task string, _ TurnOptions, events agentevents.Sink) (string, error) {
	p.tasks += task
	events.Message("done: " + task)
	return "done: " + task, nil
}

func startPlainCoder(t *testing.T) (*Server, *plainCoder) {
	t.Helper()
	pc := &plainCoder{fakeBackend: newFakeBackend()}
	srv, err := Start(Options{Backend: pc, Version: "t"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv, pc
}
