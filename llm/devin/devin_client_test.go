/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package devin

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// fakeAPI is an in-memory API for provider-adapter tests. GetSession replays
// the scripted states in order (sticking on the last one); messages accrue as
// SendMessage/CreateSession are called.
type fakeAPI struct {
	mu           sync.Mutex
	version      string
	states       []SessionState
	stateIdx     int
	messages     []Message
	created      []CreateSessionRequest
	sent         []string
	pullRequests []PullRequest
	// autoReply is appended as a Devin message the first time GetSession
	// reports StateBlocked, mimicking Devin answering then waiting.
	autoReply string
	replied   bool
}

func (f *fakeAPI) Version() string {
	if f.version == "" {
		return "v1"
	}
	return f.version
}

func (f *fakeAPI) CreateSession(_ context.Context, req CreateSessionRequest) (*Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, req)
	f.messages = append(f.messages, Message{FromUser: true, Text: req.Prompt})
	return &Session{ID: "devin-t1", URL: "https://app/devin-t1", State: StateWorking}, nil
}

func (f *fakeAPI) GetSession(_ context.Context, id string) (*Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := f.states[f.stateIdx]
	if f.stateIdx < len(f.states)-1 {
		f.stateIdx++
	}
	if state == StateBlocked && f.autoReply != "" && !f.replied {
		f.replied = true
		f.messages = append(f.messages, Message{FromUser: false, Text: f.autoReply})
	}
	msgs := make([]Message, len(f.messages))
	copy(msgs, f.messages)
	return &Session{ID: id, URL: "https://app/" + id, State: state, Messages: msgs, PullRequests: f.pullRequests}, nil
}

func (f *fakeAPI) ListSessions(context.Context, ListSessionsOptions) ([]Session, error) {
	return nil, nil
}

func (f *fakeAPI) SendMessage(_ context.Context, _ string, message string, _ []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, message)
	f.messages = append(f.messages, Message{FromUser: true, Text: message})
	return nil
}

func (f *fakeAPI) ListMessages(_ context.Context, _ string) ([]Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	msgs := make([]Message, len(f.messages))
	copy(msgs, f.messages)
	return msgs, nil
}

func (f *fakeAPI) TerminateSession(context.Context, string) error { return nil }
func (f *fakeAPI) ArchiveSession(context.Context, string) error   { return nil }
func (f *fakeAPI) SetSessionTags(context.Context, string, []string) error {
	return nil
}
func (f *fakeAPI) UploadAttachment(context.Context, string, io.Reader) (*Attachment, error) {
	return &Attachment{URL: "https://files/up"}, nil
}
func (f *fakeAPI) ListSecrets(context.Context) ([]Secret, error) { return nil, nil }
func (f *fakeAPI) CreateSecret(context.Context, CreateSecretRequest) (*Secret, error) {
	return nil, nil
}
func (f *fakeAPI) DeleteSecret(context.Context, string) error          { return nil }
func (f *fakeAPI) ListKnowledge(context.Context) ([]KnowledgeNote, error) { return nil, nil }
func (f *fakeAPI) CreateKnowledge(context.Context, KnowledgeNoteRequest) (*KnowledgeNote, error) {
	return nil, nil
}
func (f *fakeAPI) UpdateKnowledge(context.Context, string, KnowledgeNoteRequest) (*KnowledgeNote, error) {
	return nil, nil
}
func (f *fakeAPI) DeleteKnowledge(context.Context, string) error   { return nil }
func (f *fakeAPI) ListPlaybooks(context.Context) ([]Playbook, error) { return nil, nil }
func (f *fakeAPI) GetPlaybook(context.Context, string) (*Playbook, error) {
	return nil, nil
}
func (f *fakeAPI) CreatePlaybook(context.Context, PlaybookRequest) (*Playbook, error) {
	return nil, nil
}
func (f *fakeAPI) UpdatePlaybook(context.Context, string, PlaybookRequest) (*Playbook, error) {
	return nil, nil
}
func (f *fakeAPI) DeletePlaybook(context.Context, string) error { return nil }

func fastPolling(t *testing.T) {
	t.Helper()
	t.Setenv(config.DevinPollIntervalEnv, "1ms")
	t.Setenv(config.DevinTurnTimeoutEnv, "2s")
}

func TestDevinClientFirstTurnCreatesSession(t *testing.T) {
	fastPolling(t)
	api := &fakeAPI{states: []SessionState{StateWorking, StateBlocked}, autoReply: "what repo should I use?"}
	c := NewDevinClient(api, "devin", zap.NewNop())

	out, err := c.SendPrompt(context.Background(), "build the feature", nil, 0)
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if len(api.created) != 1 || api.created[0].Prompt != "build the feature" {
		t.Fatalf("session not created with the prompt: %+v", api.created)
	}
	if !strings.Contains(out, "devin-t1") {
		t.Fatalf("first reply must announce the session, got: %q", out)
	}
	if !strings.Contains(out, "what repo should I use?") {
		t.Fatalf("reply must include Devin's message, got: %q", out)
	}
	if c.SessionID() != "devin-t1" {
		t.Fatalf("SessionID = %q", c.SessionID())
	}
}

func TestDevinClientFollowUpSendsMessage(t *testing.T) {
	fastPolling(t)
	api := &fakeAPI{states: []SessionState{StateBlocked}, autoReply: "using repo X, done"}
	c := NewDevinClient(api, "devin", zap.NewNop())
	c.sessionID = "devin-t1"

	history := []models.Message{
		{Role: "user", Content: "build the feature"},
		{Role: "assistant", Content: "what repo should I use?"},
	}
	out, err := c.SendPrompt(context.Background(), "use repo X", history, 0)
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if len(api.created) != 0 {
		t.Fatal("follow-up must not create a new session")
	}
	if len(api.sent) != 1 || api.sent[0] != "use repo X" {
		t.Fatalf("follow-up not sent: %+v", api.sent)
	}
	if strings.Contains(out, "https://app/devin-t1") && strings.Contains(out, "devin-t1\n") {
		t.Fatalf("follow-up must not re-announce the session: %q", out)
	}
}

func TestDevinClientClearedConversationStartsFresh(t *testing.T) {
	fastPolling(t)
	api := &fakeAPI{states: []SessionState{StateBlocked}, autoReply: "ok"}
	c := NewDevinClient(api, "devin", zap.NewNop())
	c.sessionID = "devin-old"

	// No assistant turns in history == /newsession happened.
	if _, err := c.SendPrompt(context.Background(), "new task", []models.Message{{Role: "user", Content: "new task"}}, 0); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if len(api.created) != 1 {
		t.Fatalf("cleared conversation must create a session, created=%d", len(api.created))
	}
	if c.SessionID() != "devin-t1" {
		t.Fatalf("session id must be replaced, got %q", c.SessionID())
	}
}

func TestDevinClientLostSessionSeedsTranscript(t *testing.T) {
	fastPolling(t)
	api := &fakeAPI{states: []SessionState{StateBlocked}, autoReply: "resuming"}
	c := NewDevinClient(api, "devin", zap.NewNop())

	history := []models.Message{
		{Role: "user", Content: "original task"},
		{Role: "assistant", Content: "first answer"},
	}
	if _, err := c.SendPrompt(context.Background(), "continue", history, 0); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if len(api.created) != 1 {
		t.Fatal("lost session id must trigger a recovery session")
	}
	prompt := api.created[0].Prompt
	if !strings.Contains(prompt, "original task") || !strings.Contains(prompt, "first answer") || !strings.Contains(prompt, "continue") {
		t.Fatalf("recovery prompt must carry the transcript, got: %q", prompt)
	}
}

func TestDevinClientErrorState(t *testing.T) {
	fastPolling(t)
	api := &fakeAPI{states: []SessionState{StateError}}
	c := NewDevinClient(api, "devin", zap.NewNop())
	if _, err := c.SendPrompt(context.Background(), "task", nil, 0); err == nil {
		t.Fatal("expected an error for an errored session")
	}
}

func TestDevinClientTurnTimeout(t *testing.T) {
	t.Setenv(config.DevinPollIntervalEnv, "1ms")
	t.Setenv(config.DevinTurnTimeoutEnv, "20ms")
	api := &fakeAPI{states: []SessionState{StateWorking}}
	c := NewDevinClient(api, "devin", zap.NewNop())
	if _, err := c.SendPrompt(context.Background(), "task", nil, 0); err == nil {
		t.Fatal("expected a timeout error")
	}
}

func TestDevinMode(t *testing.T) {
	cases := map[string]string{
		"devin": "", "devin-normal": "normal", "devin-fast": "fast",
		"devin-lite": "lite", "devin-ultra": "ultra", "devin-fusion": "fusion",
		"weird": "",
	}
	for model, want := range cases {
		if got := devinMode(model); got != want {
			t.Errorf("devinMode(%q) = %q, want %q", model, got, want)
		}
	}
}
