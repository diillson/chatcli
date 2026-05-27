package hub

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(newTestStore(t), nil, 8)
}

func recv(t *testing.T, ch <-chan models.ConversationEvent) models.ConversationEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("stream closed unexpectedly")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return models.ConversationEvent{}
	}
}

func TestSubscribeDeliversBacklogThenLive(t *testing.T) {
	m := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conv, _ := m.Resolve(ctx, "alice")

	// Backlog: two events before anyone subscribes.
	_, _ = m.Append(ctx, models.ConversationEvent{ConvID: conv, Principal: "alice", Channel: "telegram", Role: models.ConvRoleUser, Content: "from telegram 1"})
	_, _ = m.Append(ctx, models.ConversationEvent{ConvID: conv, Principal: "alice", Channel: "telegram", Role: models.ConvRoleAssistant, Content: "reply 1"})

	stream, err := m.Subscribe(ctx, conv, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	b1 := recv(t, stream)
	b2 := recv(t, stream)
	if b1.Content != "from telegram 1" || b2.Content != "reply 1" {
		t.Fatalf("backlog out of order: %q, %q", b1.Content, b2.Content)
	}

	// Live: a notebook now appends and the subscriber sees it.
	_, _ = m.Append(ctx, models.ConversationEvent{ConvID: conv, Principal: "alice", Channel: "local", Role: models.ConvRoleUser, Content: "live from notebook"})
	live := recv(t, stream)
	if live.Content != "live from notebook" || live.Channel != "local" {
		t.Fatalf("unexpected live event: %+v", live)
	}
}

func TestSubscribeSinceSeqSkipsBacklog(t *testing.T) {
	m := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conv, _ := m.Resolve(ctx, "alice")

	var seqs []int64
	for i := 0; i < 3; i++ {
		ev, _ := m.Append(ctx, models.ConversationEvent{ConvID: conv, Principal: "alice", Channel: "local", Role: models.ConvRoleUser, Content: fmt.Sprintf("m%d", i)})
		seqs = append(seqs, ev.Seq)
	}

	stream, _ := m.Subscribe(ctx, conv, seqs[1]) // resume after the 2nd event
	got := recv(t, stream)
	if got.Seq != seqs[2] {
		t.Fatalf("expected to resume at seq %d, got %d", seqs[2], got.Seq)
	}
}

func TestTwoSubscribersBothReceive(t *testing.T) {
	m := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conv, _ := m.Resolve(ctx, "alice")

	s1, _ := m.Subscribe(ctx, conv, 0)
	s2, _ := m.Subscribe(ctx, conv, 0)

	_, _ = m.Append(ctx, models.ConversationEvent{ConvID: conv, Principal: "alice", Channel: "telegram", Role: models.ConvRoleUser, Content: "broadcast"})

	if got := recv(t, s1); got.Content != "broadcast" {
		t.Fatalf("s1 missed: %q", got.Content)
	}
	if got := recv(t, s2); got.Content != "broadcast" {
		t.Fatalf("s2 missed: %q", got.Content)
	}
}

func TestSubscribeCancelCleansUp(t *testing.T) {
	m := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	conv, _ := m.Resolve(context.Background(), "alice")

	stream, _ := m.Subscribe(ctx, conv, 0)
	cancel()

	// Stream must close after cancellation.
	select {
	case _, ok := <-stream:
		if ok {
			// drain any in-flight, then it must close
			for range stream {
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not close after cancel")
	}

	// Subscriber registry must be empty (no leak).
	m.mu.Lock()
	n := len(m.subs[conv])
	m.mu.Unlock()
	if n != 0 {
		t.Fatalf("subscriber not cleaned up: %d remain", n)
	}
}

func TestSlowConsumerOverflowClosesStream(t *testing.T) {
	m := NewManager(newTestStore(t), nil, 2) // tiny buffer to force overflow
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conv, _ := m.Resolve(ctx, "alice")

	stream, _ := m.Subscribe(ctx, conv, 0)
	// Give the stream goroutine time to enter the live phase (backlog empty).
	time.Sleep(50 * time.Millisecond)

	// Flood without reading; beyond the buffer the subscriber is dropped.
	for i := 0; i < 50; i++ {
		_, _ = m.Append(ctx, models.ConversationEvent{ConvID: conv, Principal: "alice", Channel: "local", Role: models.ConvRoleUser, Content: fmt.Sprintf("flood-%d", i)})
	}

	// The stream must eventually close (resync signal) rather than block forever.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-stream:
			if !ok {
				return // closed as expected
			}
		case <-deadline:
			t.Fatal("overflowed stream never closed")
		}
	}
}
