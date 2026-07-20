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
