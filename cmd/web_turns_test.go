/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/cli/webui"
	"github.com/diillson/chatcli/models"
)

func user(text string) models.Message      { return models.Message{Role: "user", Content: text} }
func assistant(text string) models.Message { return models.Message{Role: "assistant", Content: text} }

// coderTurn is the shape a coder turn leaves in the history: run context,
// skills, turn context, the task, then tool exchanges whose feedback are
// user-role messages nothing flags.
func coderTurn(task string) []models.Message {
	return []models.Message{
		models.RunContextMessage("[RUN CONTEXT] workspace"),
		{Role: "user", Content: "[SKILLS] go", Meta: &models.MessageMeta{SkillNames: "go"}},
		models.TurnContextMessage("[TURN CONTEXT] now"),
		user(task),
		assistant("<tool_call name=\"@coder\" />"),
		user("ERRO: exit 1"),
		assistant("<tool_call name=\"@coder\" />"),
		user("ok"),
		assistant("done " + task),
	}
}

func concat(parts ...[]models.Message) []models.Message {
	var out []models.Message
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// webBackend is a bound web session over the fake store.
func webBackend(t *testing.T) (*rpcBackend, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	b := sessionBackend(store)
	b.bindSession("web", "web-1")
	return b, store
}

// A turn the page ran is remembered whole, however many user-role messages
// its tool exchanges left behind; turns other surfaces made are found by
// their typed messages.
func TestWebTurns_RecordAndLocate(t *testing.T) {
	b, _ := webBackend(t)
	chat1 := []models.Message{models.TurnContextMessage("[TURN CONTEXT] a"), user("hello"), assistant("hi")}
	b.sessions["web"] = chat1
	b.recordTurn("web", nil, chat1, webui.TurnRecord{Mode: "chat", Text: "hello"})

	coder := coderTurn("fix the build")
	sysMsg := models.Message{Role: "system", Content: "[CODER MODE] rules"}
	// The coder run prepends its system message: every index moves.
	after := concat([]models.Message{sysMsg}, chat1, coder)
	b.sessions["web"] = after
	b.recordTurn("web", chat1, after, webui.TurnRecord{Mode: "coder", Text: "fix the build"})

	// The terminal adds a chat turn of its own to the same session.
	foreign := []models.Message{user("and now?"), assistant("next")}
	b.sessions["web"] = concat(after, foreign)

	starts, owners := turnStarts(b.sessions["web"], b.turns["web"])
	if len(starts) != 3 || starts[0] != 1 || starts[1] != 1+len(chat1) || starts[2] != 1+len(chat1)+len(coder) {
		t.Fatalf("starts = %v", starts)
	}
	if owners[starts[0]] != 0 || owners[starts[1]] != 1 {
		t.Fatalf("owners = %v", owners)
	}
	if _, foreignOwned := owners[starts[2]]; foreignOwned {
		t.Fatal("the terminal's turn must not be attributed to a page turn")
	}

	rec, ok := b.LastTurn("web")
	if !ok || rec.Mode != "" || rec.Text != "and now?" {
		t.Fatalf("last (foreign) turn = %+v %v", rec, ok)
	}
}

// /rewind removes whole turns, newest first, persists the result to the
// bound session — empty included — and can put the turns back.
func TestWebTurns_RewindPersistsAndRestores(t *testing.T) {
	b, store := webBackend(t)
	chat1 := []models.Message{user("hello"), assistant("hi")}
	coder := coderTurn("fix the build")
	sysMsg := models.Message{Role: "system", Content: "[CODER MODE] rules"}
	full := concat([]models.Message{sysMsg}, chat1, coder)
	b.sessions["web"] = full
	b.recordTurn("web", nil, chat1, webui.TurnRecord{Mode: "chat", Text: "hello"})
	b.recordTurn("web", chat1, full, webui.TurnRecord{Mode: "coder", Text: "fix the build", Options: webui.TurnOptions{Provider: "OPENAI"}})

	rec, ok := b.LastTurn("web")
	if !ok || rec.Mode != "coder" || rec.Text != "fix the build" || rec.Options.Provider != "OPENAI" {
		t.Fatalf("last turn = %+v %v", rec, ok)
	}

	res, err := b.RewindTurns("web", 1)
	if err != nil || res.Turns != 1 || res.Messages != len(coder) {
		t.Fatalf("rewind 1 = %+v %v", res, err)
	}
	if got := b.sessions["web"]; len(got) != 1+len(chat1) || got[len(got)-1].Content != "hi" {
		t.Fatalf("live after rewind = %+v", got)
	}
	if saved := store.saved["web-1"]; len(saved) != 1+len(chat1) {
		t.Fatalf("the bound session must hold the rewound history, got %d messages", len(saved))
	}
	if rec, ok := b.LastTurn("web"); !ok || rec.Text != "hello" || rec.Mode != "chat" {
		t.Fatalf("last turn after rewind = %+v %v", rec, ok)
	}

	if !res.Restore() {
		t.Fatal("restore must succeed while nothing was added")
	}
	if len(b.sessions["web"]) != len(full) || len(store.saved["web-1"]) != len(full) {
		t.Fatalf("restore must bring the turn back live and saved: %d / %d", len(b.sessions["web"]), len(store.saved["web-1"]))
	}
	if res.Restore() {
		// A second restore has a history that already differs from the cut.
		t.Fatal("restore must apply once")
	}

	// Rewinding past the start empties the session: a lone system prompt
	// is not a conversation, and the empty history is written through.
	res, err = b.RewindTurns("web", 5)
	if err != nil || res.Turns != 2 || len(b.sessions["web"]) != 0 {
		t.Fatalf("rewind all = %+v %v live=%d", res, err, len(b.sessions["web"]))
	}
	if saved, ok := store.saved["web-1"]; !ok || len(saved) != 0 {
		t.Fatalf("the bound session must be emptied too: %v %d", ok, len(saved))
	}
	if res, _ := b.RewindTurns("web", 1); res.Turns != 0 {
		t.Fatalf("nothing left to rewind: %+v", res)
	}
	// A loaded conversation is another one: its turns are not the ones
	// remembered.
	store.saved["other"] = []models.Message{user("x"), assistant("y")}
	b.sessions["web"] = full
	b.recordTurn("web", nil, full, webui.TurnRecord{Mode: "chat", Text: "hello"})
	if _, err := b.ManageSession(context.Background(), "load", "web", "other"); err != nil || len(b.turns["web"]) != 0 {
		t.Fatalf("load must forget the previous conversation's turns: %v %d", err, len(b.turns["web"]))
	}
}

// A retry after a turn made by another surface replays its typed text and
// images.
func TestWebTurns_LastTurnFallsBackToTypedMessage(t *testing.T) {
	b, _ := webBackend(t)
	img := models.ImageContent{MediaType: "image/png", Data: []byte{1, 2}, FileName: "a.png"}
	b.sessions["web"] = []models.Message{
		models.TurnContextMessage("[TURN CONTEXT]"),
		{Role: "user", Content: "look", Images: []models.ImageContent{img}},
		assistant("seen"),
	}
	rec, ok := b.LastTurn("web")
	if !ok || rec.Text != "look" || len(rec.Options.Images) != 1 || rec.Options.Images[0].Name != "a.png" {
		t.Fatalf("fallback record = %+v %v", rec, ok)
	}
	res, err := b.RewindTurns("web", 1)
	if err != nil || res.Turns != 1 || len(b.sessions["web"]) != 0 {
		t.Fatalf("the turn context belongs to the turn: %+v %v live=%+v", res, err, b.sessions["web"])
	}
}
