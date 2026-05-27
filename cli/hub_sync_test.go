/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// fakeHubClient is an in-memory HubClient for exercising HubSync without gRPC.
type fakeHubClient struct {
	mu       sync.Mutex
	convID   string
	seq      int64
	events   []models.ConversationEvent
	subCh    chan models.ConversationEvent
	appended []models.ConversationEvent
	bindings []models.HubBinding
}

func newFakeHubClient() *fakeHubClient {
	return &fakeHubClient{convID: "conv-1", subCh: make(chan models.ConversationEvent, 16)}
}

func (f *fakeHubClient) ResolveActiveConversation(_ context.Context, _ string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.convID, "alice", nil
}

func (f *fakeHubClient) NewConversation(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.convID = "conv-2"
	f.events = nil
	return f.convID, nil
}

func (f *fakeHubClient) AppendEvent(_ context.Context, ev models.ConversationEvent) (models.ConversationEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	ev.Seq = f.seq
	f.events = append(f.events, ev)
	f.appended = append(f.appended, ev)
	return ev, nil
}

func (f *fakeHubClient) ReadConversation(_ context.Context, _ string, sinceSeq int64, _ int) ([]models.ConversationEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.ConversationEvent
	for _, ev := range f.events {
		if ev.Seq > sinceSeq {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (f *fakeHubClient) SubscribeConversation(ctx context.Context, _ string, _ int64) (<-chan models.ConversationEvent, error) {
	out := make(chan models.ConversationEvent)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-f.subCh:
				if !ok {
					return
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

func (f *fakeHubClient) SetBinding(_ context.Context, platform, userID, principal string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bindings = append(f.bindings, models.HubBinding{Platform: platform, UserID: userID, Principal: principal})
	return nil
}

func (f *fakeHubClient) ListBindings(_ context.Context, _ string) ([]models.HubBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]models.HubBinding(nil), f.bindings...), nil
}

func (f *fakeHubClient) seed(events ...models.ConversationEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range events {
		f.seq++
		ev.Seq = f.seq
		f.events = append(f.events, ev)
	}
}

func TestHubSyncHydrate(t *testing.T) {
	fc := newFakeHubClient()
	fc.seed(
		models.ConversationEvent{ConvID: "conv-1", Channel: "telegram", Role: models.ConvRoleUser, Content: "hi"},
		models.ConversationEvent{ConvID: "conv-1", Channel: "telegram", Role: models.ConvRoleAssistant, Content: "hello"},
	)
	hs := newHubSync(fc, zap.NewNop(), nil)

	msgs, err := hs.hydrate(context.Background())
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Content != "hi" || msgs[1].Role != "assistant" {
		t.Fatalf("unexpected hydrated history: %+v", msgs)
	}
	if hs.lastSeq != 2 {
		t.Fatalf("lastSeq = %d, want 2", hs.lastSeq)
	}
}

func TestHubSyncAfterChatTurnAppendsLocalChannel(t *testing.T) {
	fc := newFakeHubClient()
	hs := newHubSync(fc, zap.NewNop(), nil)
	if _, err := hs.hydrate(context.Background()); err != nil {
		t.Fatalf("hydrate: %v", err)
	}

	hs.afterChatTurn(context.Background(), "question", "answer")

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.appended) != 2 {
		t.Fatalf("expected 2 appends, got %d", len(fc.appended))
	}
	for _, ev := range fc.appended {
		if ev.Channel != hubChannelLocal {
			t.Fatalf("local turn appended with channel %q", ev.Channel)
		}
	}
	if fc.appended[0].Role != models.ConvRoleUser || fc.appended[1].Role != models.ConvRoleAssistant {
		t.Fatalf("unexpected append roles: %+v", fc.appended)
	}
}

func TestHubSyncTailRendersOtherChannelsSkipsLocal(t *testing.T) {
	fc := newFakeHubClient()

	var mu sync.Mutex
	var rendered []models.ConversationEvent
	render := func(ev models.ConversationEvent) {
		mu.Lock()
		rendered = append(rendered, ev)
		mu.Unlock()
	}
	hs := newHubSync(fc, zap.NewNop(), render)
	if _, err := hs.hydrate(context.Background()); err != nil {
		t.Fatalf("hydrate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hs.startTail(ctx)

	// A local echo (must be skipped) and a telegram message (must be rendered).
	fc.subCh <- models.ConversationEvent{ConvID: "conv-1", Seq: 10, Channel: hubChannelLocal, Role: models.ConvRoleUser, Content: "my own"}
	fc.subCh <- models.ConversationEvent{ConvID: "conv-1", Seq: 11, Channel: "telegram", Role: models.ConvRoleUser, Content: "from phone"}

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(rendered)
		mu.Unlock()
		if n >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("tail did not render the telegram event")
		case <-time.After(10 * time.Millisecond):
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(rendered) != 1 || rendered[0].Content != "from phone" {
		t.Fatalf("expected only the telegram event rendered, got %+v", rendered)
	}
}

func TestHubSyncBindAndList(t *testing.T) {
	fc := newFakeHubClient()
	hs := newHubSync(fc, zap.NewNop(), nil)
	ctx := context.Background()

	if err := hs.bind(ctx, "telegram", "u1", "alice"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	got, err := hs.bindings(ctx, "")
	if err != nil {
		t.Fatalf("bindings: %v", err)
	}
	if len(got) != 1 || got[0].Platform != "telegram" || got[0].Principal != "alice" {
		t.Fatalf("unexpected bindings: %+v", got)
	}
}

func TestHubCommandWiring(t *testing.T) {
	// /hub with no connection must not panic and must short-circuit.
	c := &ChatCLI{logger: zap.NewNop()}
	c.handleHubCommand("/hub whoami") // hubSync nil → not-connected path

	// With a fake hub, /hub bind drives SetBinding through HubSync.
	fc := newFakeHubClient()
	c.hubSync = newHubSync(fc, zap.NewNop(), c.renderTailedEvent)
	c.handleHubCommand("/hub bind telegram 123 alice")

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.bindings) != 1 || fc.bindings[0].UserID != "123" || fc.bindings[0].Principal != "alice" {
		t.Fatalf("/hub bind did not persist: %+v", fc.bindings)
	}
}

func TestHubSyncNewSessionRotates(t *testing.T) {
	fc := newFakeHubClient()
	hs := newHubSync(fc, zap.NewNop(), nil)
	if _, err := hs.hydrate(context.Background()); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if hs.convID != "conv-1" {
		t.Fatalf("pre-rotate convID = %q", hs.convID)
	}
	if err := hs.newSession(context.Background()); err != nil {
		t.Fatalf("newSession: %v", err)
	}
	if hs.convID != "conv-2" || hs.lastSeq != 0 {
		t.Fatalf("after rotate convID=%q lastSeq=%d, want conv-2/0", hs.convID, hs.lastSeq)
	}
}
