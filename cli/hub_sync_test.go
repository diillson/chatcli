/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// fakeHubClient is an in-memory HubClient for exercising HubSync without gRPC.
type fakeHubClient struct {
	mu          sync.Mutex
	convID      string
	convCounter int
	seq         int64
	events      []models.ConversationEvent
	subCh       chan models.ConversationEvent
	appended    []models.ConversationEvent
	bindings    []models.HubBinding
}

func newFakeHubClient() *fakeHubClient {
	return &fakeHubClient{convID: "conv-1", convCounter: 1, subCh: make(chan models.ConversationEvent, 16)}
}

func (f *fakeHubClient) ResolveActiveConversation(_ context.Context, _ string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.convID, "alice", nil
}

func (f *fakeHubClient) NewConversation(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.convCounter++
	f.convID = fmt.Sprintf("conv-%d", f.convCounter)
	f.events = nil // ephemeral: rotating prunes the prior thread
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
		<-ctx.Done()
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

// seed appends events to the current conversation (used to simulate turns
// written by another channel between local turns).
func (f *fakeHubClient) seed(events ...models.ConversationEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range events {
		f.seq++
		ev.Seq = f.seq
		f.events = append(f.events, ev)
	}
}

func TestHubSyncStartFreshRotates(t *testing.T) {
	fc := newFakeHubClient()
	hs := newHubSync(fc, zap.NewNop())

	if err := hs.startFresh(context.Background()); err != nil {
		t.Fatalf("startFresh: %v", err)
	}
	// Ephemeral: a fresh conversation is created (rotated away from conv-1).
	if hs.convID == "conv-1" || hs.convID == "" {
		t.Fatalf("startFresh did not rotate to a fresh conversation: %q", hs.convID)
	}
	if hs.principal != "alice" || hs.lastSeq != 0 {
		t.Fatalf("unexpected state principal=%q lastSeq=%d", hs.principal, hs.lastSeq)
	}
}

func TestHubSyncMirrorTurnAppendsLocalChannel(t *testing.T) {
	fc := newFakeHubClient()
	hs := newHubSync(fc, zap.NewNop())
	if err := hs.startFresh(context.Background()); err != nil {
		t.Fatalf("startFresh: %v", err)
	}

	hs.mirrorTurn(context.Background(), "question", "answer")

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

func TestHubSyncPullSkipsLocalReturnsOthers(t *testing.T) {
	fc := newFakeHubClient()
	hs := newHubSync(fc, zap.NewNop())
	if err := hs.startFresh(context.Background()); err != nil {
		t.Fatalf("startFresh: %v", err)
	}

	// A local echo (skip) and a telegram turn (deliver) land on the conversation.
	fc.seed(
		models.ConversationEvent{Channel: hubChannelLocal, Role: models.ConvRoleUser, Content: "my own"},
		models.ConversationEvent{Channel: "telegram", Role: models.ConvRoleUser, Content: "from phone"},
	)

	msgs := hs.pull(context.Background())
	if len(msgs) != 1 || msgs[0].Content != "from phone" {
		t.Fatalf("pull should return only the telegram turn, got %+v", msgs)
	}
	// A second pull returns nothing new (watermark advanced past both).
	if more := hs.pull(context.Background()); len(more) != 0 {
		t.Fatalf("second pull should be empty, got %+v", more)
	}
}

func TestHubSyncBindAndList(t *testing.T) {
	fc := newFakeHubClient()
	hs := newHubSync(fc, zap.NewNop())
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

	// With a fake hub, drive each subcommand branch.
	fc := newFakeHubClient()
	c.hubSync = newHubSync(fc, zap.NewNop())
	_ = c.hubSync.startFresh(context.Background()) // populate convID/principal for whoami
	c.handleHubCommand("/hub whoami")
	c.handleHubCommand("/hub bind telegram 123 alice")
	c.handleHubCommand("/hub bind")     // usage branch
	c.handleHubCommand("/hub bindings") // list branch
	c.handleHubCommand("/hub bogus")    // default/usage branch

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.bindings) != 1 || fc.bindings[0].UserID != "123" || fc.bindings[0].Principal != "alice" {
		t.Fatalf("/hub bind did not persist: %+v", fc.bindings)
	}
}

func TestShowConfigHub(t *testing.T) {
	// Without a connection.
	c := &ChatCLI{logger: zap.NewNop()}
	c.showConfigHub()

	// With a live sync (covers the connected branch).
	fc := newFakeHubClient()
	c.hubSync = newHubSync(fc, zap.NewNop())
	_ = c.hubSync.startFresh(context.Background())
	t.Setenv("CHATCLI_HUB_ENABLED", "false") // exercise the disabled-label path
	c.showConfigHub()
}

func TestHubSyncNewSessionRotates(t *testing.T) {
	fc := newFakeHubClient()
	hs := newHubSync(fc, zap.NewNop())
	if err := hs.startFresh(context.Background()); err != nil {
		t.Fatalf("startFresh: %v", err)
	}
	before := hs.convID
	if err := hs.newSession(context.Background()); err != nil {
		t.Fatalf("newSession: %v", err)
	}
	if hs.convID == before || hs.lastSeq != 0 {
		t.Fatalf("after rotate convID=%q (was %q) lastSeq=%d", hs.convID, before, hs.lastSeq)
	}
}
