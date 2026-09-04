/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/persona"
	"go.uber.org/zap"
)

// i18n is initialized by TestMain in config_sections_test.go.

// rpcChatFakeClient captures the tempHistory it receives so tests can assert
// the full pipeline actually assembled the system prompt.
type rpcChatFakeClient struct {
	reply    string
	err      error
	lastHist []models.Message
}

func (f *rpcChatFakeClient) GetModelName() string { return "fake-model" }
func (f *rpcChatFakeClient) SendPrompt(_ context.Context, _ string, hist []models.Message, _ int) (string, error) {
	f.lastHist = append([]models.Message(nil), hist...)
	return f.reply, f.err
}

// newRPCChatCLI builds a ChatCLI with the pieces RunChatTurnRPC touches:
// context handler (HOME redirected to a tempdir), persona/skill handlers with
// one trigger skill, history compactor, and a capture-everything fake client.
func newRPCChatCLI(t *testing.T, fake *rpcChatFakeClient) *ChatCLI {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, ".agent", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	skill := `---
name: zeta-skill
description: fires on the word zeta
triggers: ["zeta"]
---
ZETA-SKILL-BODY
`
	if err := os.WriteFile(filepath.Join(skillsDir, "zeta-skill.md"), []byte(skill), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	mgr := persona.NewManager(zap.NewNop())
	// Skill activations are recorded asynchronously under HOME, which here
	// is a t.TempDir: without draining them the write races the directory
	// removal and fails the test with "directory not empty". Registered
	// after the TempDir cleanups so it runs before them (LIFO).
	t.Cleanup(mgr.WaitUsageFlush)
	mgr.SetProjectDir(tmp)
	if _, err := mgr.RefreshSkills(); err != nil {
		t.Fatalf("RefreshSkills: %v", err)
	}

	ch, err := NewContextHandler(zap.NewNop())
	if err != nil {
		t.Fatalf("NewContextHandler: %v", err)
	}

	c := &ChatCLI{
		logger:           zap.NewNop(),
		Client:           fake,
		Provider:         "FAKE",
		Model:            "fake-model",
		historyCompactor: NewHistoryCompactor(zap.NewNop()),
		contextHandler:   ch,
		personaHandler:   &PersonaHandler{manager: mgr, logger: zap.NewNop()},
	}
	c.skillHandler = NewSkillHandler(zap.NewNop(), mgr)
	return c
}

func TestRunChatTurnRPC_FullPipelineParity(t *testing.T) {
	fake := &rpcChatFakeClient{reply: "hello from fake"}
	c := newRPCChatCLI(t, fake)
	c.history = []models.Message{{Role: "user", Content: "repl-turn"}}

	prior := []models.Message{
		{Role: "user", Content: "mcp turn 1"},
		{Role: "assistant", Content: "reply 1"},
	}
	turn, err := c.RunChatTurnRPC(context.Background(), "sess-a", "tell me about zeta please", prior, RPCChatOpts{})
	if err != nil {
		t.Fatalf("RunChatTurnRPC: %v", err)
	}
	if turn.Reply != "hello from fake" {
		t.Fatalf("reply = %q", turn.Reply)
	}

	// The fake must have received a system message assembled by the full
	// pipeline: mode banner + the trigger-activated skill body.
	if len(fake.lastHist) == 0 || fake.lastHist[0].Role != "system" {
		t.Fatalf("first message sent to the LLM must be the assembled system prompt; got %+v", fake.lastHist)
	}
	// Trigger-activated skills are per-turn context: they ride in the
	// flagged turn context message right before the user's turn, never in
	// the (byte-stable) system message.
	var sent strings.Builder
	for _, m := range fake.lastHist {
		if m.IsTurnContext() {
			sent.WriteString(m.Content)
		}
	}
	if !strings.Contains(sent.String(), "ZETA-SKILL-BODY") && !strings.Contains(sent.String(), "zeta-skill") {
		t.Errorf("trigger-activated skill missing from the turn context:\n%v", fake.lastHist)
	}
	if strings.Contains(fake.lastHist[0].Content, "ZETA-SKILL-BODY") {
		t.Error("auto skills must not be in the system message any more")
	}
	if len(fake.lastHist[0].SystemParts) == 0 {
		t.Error("SystemParts must be populated (Anthropic cache-control path)")
	}
	// Prior session history must precede the new user turn.
	joined := ""
	for _, m := range fake.lastHist {
		joined += m.Role + ":" + m.Content + "\n"
	}
	if !strings.Contains(joined, "mcp turn 1") || !strings.Contains(joined, "reply 1") {
		t.Errorf("prior session history missing from the LLM call:\n%s", joined)
	}

	// Returned history = prior + user + assistant, plus the flagged turn
	// context message the pipeline persists so the next request replays
	// the same bytes (it is not user text and is excluded from the count).
	real := 0
	for _, m := range turn.History {
		if !m.IsTurnContext() {
			real++
		}
	}
	if real != len(prior)+2 {
		t.Fatalf("history len = %d (non-context), want %d", real, len(prior)+2)
	}
	last := turn.History[len(turn.History)-1]
	if last.Role != "assistant" || last.Content != "hello from fake" {
		t.Errorf("last history entry = %+v", last)
	}

	// REPL state must be untouched.
	if len(c.history) != 1 || c.history[0].Content != "repl-turn" {
		t.Errorf("cli.history not restored: %+v", c.history)
	}
	if c.currentSessionName != "" {
		t.Errorf("currentSessionName not restored: %q", c.currentSessionName)
	}
}

func TestRunChatTurnRPC_RestoresStateOnError(t *testing.T) {
	fake := &rpcChatFakeClient{err: errors.New("provider down")}
	c := newRPCChatCLI(t, fake)
	c.history = []models.Message{{Role: "user", Content: "repl-turn"}}
	c.currentSessionName = "repl-session"

	_, err := c.RunChatTurnRPC(context.Background(), "sess-b", "hi", nil, RPCChatOpts{})
	if err == nil {
		t.Fatal("expected the provider error to propagate")
	}
	if len(c.history) != 1 || c.history[0].Content != "repl-turn" {
		t.Errorf("cli.history not restored on error: %+v", c.history)
	}
	if c.currentSessionName != "repl-session" {
		t.Errorf("currentSessionName not restored on error: %q", c.currentSessionName)
	}
}

func TestRunChatTurnRPC_SessionScopesAttachedContexts(t *testing.T) {
	fake := &rpcChatFakeClient{reply: "ok"}
	c := newRPCChatCLI(t, fake)

	// Create and attach a context to session "sess-ctx" only.
	dataDir := t.TempDir()
	f := filepath.Join(dataDir, "notes.txt")
	if err := os.WriteFile(f, []byte("SECRET-CONTEXT-MARKER content for the model"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := c.contextHandler.GetManager()
	fc, err := mgr.CreateContext(context.Background(), "notes", "test notes", []string{f}, "full", nil, false)
	if err != nil {
		t.Fatalf("CreateContext: %v", err)
	}
	if err := mgr.AttachContext("sess-ctx", fc.ID, 0); err != nil {
		t.Fatalf("AttachContext: %v", err)
	}

	// Turn on the attached session: marker must be in the system prompt.
	if _, err := c.RunChatTurnRPC(context.Background(), "sess-ctx", "hi", nil, RPCChatOpts{}); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if len(fake.lastHist) == 0 || !strings.Contains(fake.lastHist[0].Content, "SECRET-CONTEXT-MARKER") {
		t.Error("attached context missing from the session it was attached to")
	}

	// Turn on another session: marker must NOT leak.
	if _, err := c.RunChatTurnRPC(context.Background(), "other-session", "hi", nil, RPCChatOpts{}); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if len(fake.lastHist) > 0 && fake.lastHist[0].Role == "system" &&
		strings.Contains(fake.lastHist[0].Content, "SECRET-CONTEXT-MARKER") {
		t.Error("attached context leaked into a different session")
	}
}
