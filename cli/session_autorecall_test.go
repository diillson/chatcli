/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func newSessionRecallCLI(t *testing.T) *ChatCLI {
	t.Helper()
	c := &ChatCLI{sessionManager: newTestSessionManager(t), logger: zap.NewNop()}
	if err := c.sessionManager.SaveSessionV2("autosave-20260719-1800", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "user", Content: "o delegate do @model não repassa o prompt"},
			{Role: "assistant", Content: "O flattener entrega o prompt como um elemento do argv; o parser re-splitava por espaço."},
			{Role: "assistant", Content: "Corrigido o parser do delegate e o spinner do turnTimer."},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestSessionAutoRecall_ReferentialQuestion reproduces the reported gap: the
// user comes back and asks about the previous session — the block must point
// at the matching autosave without the model having to know @session exists.
func TestSessionAutoRecall_ReferentialQuestion(t *testing.T) {
	c := newSessionRecallCLI(t)

	block := c.sessionAutoRecallBlock(nil, "o que a gente fez na sessão anterior sobre o delegate?")
	if block == "" {
		t.Fatal("referential question about past work must produce a recall block")
	}
	if !strings.Contains(block, "[SESSION RECALL]") || !strings.Contains(block, "autosave-20260719-1800") {
		t.Errorf("block must name the matching session: %s", block)
	}
	if !strings.Contains(block, "@session get") {
		t.Errorf("block must teach the follow-up tool call: %s", block)
	}
	if !strings.Contains(block, "delegate do @model") {
		t.Errorf("block should carry the derived title: %s", block)
	}
}

// TestSessionAutoRecall_HintDriven: matching topic hints surface the session
// even without an explicit reference, but only with enough evidence.
func TestSessionAutoRecall_HintDriven(t *testing.T) {
	c := newSessionRecallCLI(t)

	if block := c.sessionAutoRecallBlock([]string{"delegate", "prompt", "flattener"}, "seguir com a correção"); block == "" {
		t.Error("strong hint overlap should surface the session")
	}
	if block := c.sessionAutoRecallBlock([]string{"kubernetes", "ingress"}, "novo assunto"); block != "" {
		t.Errorf("unrelated hints must stay silent, got: %s", block)
	}
}

// TestSessionAutoRecall_SkipsLiveSession: the session already in context must
// not be recalled onto itself.
func TestSessionAutoRecall_SkipsLiveSession(t *testing.T) {
	c := newSessionRecallCLI(t)
	c.currentSessionName = "autosave-20260719-1800"

	if block := c.sessionAutoRecallBlock(nil, "o que discutimos sobre o delegate na última sessão?"); block != "" {
		t.Errorf("live session must be excluded from recall, got: %s", block)
	}
}

// TestSessionAutoRecall_GateOff: the kill switch silences the block.
func TestSessionAutoRecall_GateOff(t *testing.T) {
	c := newSessionRecallCLI(t)
	t.Setenv("CHATCLI_SESSION_AUTORECALL", "off")

	if block := c.sessionAutoRecallBlock(nil, "o que fizemos na sessão anterior?"); block != "" {
		t.Errorf("gate off must silence recall, got: %s", block)
	}
}

func TestLastUserMessage(t *testing.T) {
	history := []models.Message{
		{Role: "user", Content: "primeira"},
		{Role: "assistant", Content: "resposta"},
		{Role: "user", Content: "última pergunta"},
		{Role: "assistant", Content: "outra resposta"},
	}
	if got := lastUserMessage(history); got != "última pergunta" {
		t.Errorf("lastUserMessage = %q", got)
	}
	if got := lastUserMessage(nil); got != "" {
		t.Errorf("empty history must yield empty, got %q", got)
	}
}

// TestSessionAutoRecall_TwoWeeksOld: an explicit recall question must reach a
// session far outside the ambient recent window, and a temporal expression
// must scope the search to that date range.
func TestSessionAutoRecall_TwoWeeksOld(t *testing.T) {
	c := &ChatCLI{sessionManager: newTestSessionManager(t), logger: zap.NewNop()}
	if err := c.sessionManager.SaveSessionV2("autosave-old-mcp-oauth", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "user", Content: "implementar oauth para servidores mcp remotos"},
			{Role: "assistant", Content: "Discovery RFC 9728 mais DCR e PKCE loopback implementados."},
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Age it two weeks and bury it behind many fresh unrelated sessions so
	// the ambient window alone would never see it.
	past := time.Now().Add(-14 * 24 * time.Hour)
	if err := os.Chtimes(c.sessionManager.getSessionPath("autosave-old-mcp-oauth"), past, past); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 35; i++ {
		name := fmt.Sprintf("autosave-fresh-%02d", i)
		if err := c.sessionManager.SaveSessionV2(name, &SessionData{
			Version:     2,
			ChatHistory: []models.Message{{Role: "user", Content: fmt.Sprintf("assunto irrelevante número %d", i)}},
		}); err != nil {
			t.Fatal(err)
		}
	}

	block := c.sessionAutoRecallBlock(nil, "lembra do que fizemos sobre oauth no mcp há 2 semanas?")
	if !strings.Contains(block, "autosave-old-mcp-oauth") {
		t.Errorf("explicit recall must reach the two-week-old session, got: %s", block)
	}

	// Ambient hints alone must NOT reach past the recent window.
	if block := c.sessionAutoRecallBlock([]string{"oauth", "mcp", "remotos"}, "seguindo aqui"); strings.Contains(block, "autosave-old-mcp-oauth") {
		t.Errorf("ambient recall must stay within the recent window, got: %s", block)
	}
}

// TestSessionAutoRecall_ReferenceTime: with CHATCLI_NOW pinned, "two weeks
// ago" is measured from the pinned date, so a conversation replayed from the
// past is recalled against the day it happened rather than the day it runs.
func TestSessionAutoRecall_ReferenceTime(t *testing.T) {
	c := &ChatCLI{sessionManager: newTestSessionManager(t), logger: zap.NewNop()}
	if err := c.sessionManager.SaveSessionV2("replay-oauth", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "user", Content: "implementar oauth para servidores mcp remotos"},
			{Role: "assistant", Content: "Discovery RFC 9728 mais DCR e PKCE loopback implementados."},
		},
	}); err != nil {
		t.Fatal(err)
	}
	held := time.Date(2023, 5, 16, 15, 0, 0, 0, time.Local)
	if err := os.Chtimes(c.sessionManager.getSessionPath("replay-oauth"), held, held); err != nil {
		t.Fatal(err)
	}
	const question = "lembra do que fizemos sobre oauth no mcp há 2 semanas?"

	t.Setenv("CHATCLI_NOW", "2023-05-30T10:00:00")
	if block := c.sessionAutoRecallBlock(nil, question); !strings.Contains(block, "replay-oauth") {
		t.Errorf("two weeks before the pinned date must reach the session, got: %s", block)
	}

	// Control: against the wall clock the same words point years later.
	t.Setenv("CHATCLI_NOW", "")
	if block := c.sessionAutoRecallBlock(nil, question); strings.Contains(block, "replay-oauth") {
		t.Errorf("without a pinned date the window is relative to today, got: %s", block)
	}
}

func TestSessionRecencyBoost_ReferenceTime(t *testing.T) {
	saved := time.Date(2023, 5, 30, 0, 0, 0, 0, time.UTC)
	t.Setenv("CHATCLI_NOW", "2023-05-30T00:00:00Z")
	if got := sessionRecencyBoost(saved); got != 1.25 {
		t.Errorf("a session saved at the pinned date is the newest: boost = %v, want 1.25", got)
	}
	t.Setenv("CHATCLI_NOW", "")
	if got := sessionRecencyBoost(saved); got >= 1.01 {
		t.Errorf("against the wall clock the 2023 session is old: boost = %v", got)
	}
}

// The age the model reads is measured from CHATCLI_NOW: a replayed 2023
// conversation must not report a gap of years to the wall clock.
func TestFormatSessionAge_ReferenceTime(t *testing.T) {
	saved := time.Date(2023, 4, 16, 10, 0, 0, 0, time.UTC)
	t.Setenv("CHATCLI_NOW", "2023-05-07T10:00:00Z")
	if got := formatSessionAge(saved); got != "21d ago" {
		t.Errorf("age from the pinned date = %q, want 21d ago", got)
	}
	t.Setenv("CHATCLI_NOW", "2023-04-16T10:30:00Z")
	if got := formatSessionAge(saved); got != "30m ago" {
		t.Errorf("got %q, want 30m ago", got)
	}
	// A save after the reference time (written during the run) is not negative.
	t.Setenv("CHATCLI_NOW", "2023-04-15T10:00:00Z")
	if got := formatSessionAge(saved); got != "0m ago" {
		t.Errorf("future save = %q, want 0m ago", got)
	}
	t.Setenv("CHATCLI_NOW", "")
	if got := formatSessionAge(saved); got == "21d ago" {
		t.Errorf("against the wall clock the age is years, got %q", got)
	}
}

// A label that counts calendar days: saved on the 15th in the evening and
// asked about early on the 18th is 3 days ago, not the 2 that flooring the
// 55 elapsed hours gives. A save late one evening and a question early the
// next morning is yesterday.
func TestFormatSessionAge_CalendarDays(t *testing.T) {
	cases := []struct {
		saved, now time.Time
		want       string
	}{
		{time.Date(2023, 4, 15, 18, 11, 0, 0, time.UTC), time.Date(2023, 4, 18, 1, 48, 0, 0, time.UTC), "3d ago"},
		{time.Date(2023, 4, 15, 8, 0, 0, 0, time.UTC), time.Date(2023, 4, 18, 22, 0, 0, 0, time.UTC), "3d ago"},
		{time.Date(2023, 4, 17, 23, 30, 0, 0, time.UTC), time.Date(2023, 4, 18, 9, 30, 0, 0, time.UTC), "yesterday"},
		{time.Date(2023, 4, 18, 3, 0, 0, 0, time.UTC), time.Date(2023, 4, 18, 9, 30, 0, 0, time.UTC), "6h ago"},
		{time.Date(2023, 4, 16, 10, 0, 0, 0, time.UTC), time.Date(2023, 5, 7, 10, 0, 0, 0, time.UTC), "21d ago"},
		{time.Date(2023, 2, 27, 20, 0, 0, 0, time.UTC), time.Date(2023, 3, 1, 8, 0, 0, 0, time.UTC), "2d ago"},
	}
	for _, c := range cases {
		if got := formatSessionAgeAt(c.saved, c.now); got != c.want {
			t.Errorf("saved %s, now %s: %q, want %q", c.saved, c.now, got, c.want)
		}
	}
}
