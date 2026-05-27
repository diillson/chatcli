package gateway

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// fakeAdapter emits a fixed set of messages then blocks until ctx is done.
type fakeAdapter struct {
	name string
	emit []InboundMessage
	mu   sync.Mutex
	sent []OutboundMessage
}

func (f *fakeAdapter) Name() string { return f.name }

func (f *fakeAdapter) Start(ctx context.Context, inbound chan<- InboundMessage) error {
	for _, m := range f.emit {
		select {
		case inbound <- m:
		case <-ctx.Done():
			return nil
		}
	}
	<-ctx.Done()
	return nil
}

func (f *fakeAdapter) Send(_ context.Context, msg OutboundMessage) error {
	f.mu.Lock()
	f.sent = append(f.sent, msg)
	f.mu.Unlock()
	return nil
}

func (f *fakeAdapter) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func TestRunner_RoutesAndReplies(t *testing.T) {
	fa := &fakeAdapter{
		name: "fake",
		emit: []InboundMessage{
			{Platform: "fake", ChatID: "1", UserID: "u", Text: "hello"},
			{Platform: "fake", ChatID: "2", UserID: "u", Text: "world"},
		},
	}
	agent := func(_ context.Context, session, text string) (string, error) {
		return "echo:" + text, nil
	}
	r := NewRunner([]Adapter{fa}, agent, zap.NewNop(), 2)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = r.Run(ctx); close(done) }()

	// Wait for both replies to be delivered.
	deadline := time.After(2 * time.Second)
	for fa.sentCount() < 2 {
		select {
		case <-deadline:
			t.Fatalf("timed out; got %d replies", fa.sentCount())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done

	fa.mu.Lock()
	defer fa.mu.Unlock()
	if len(fa.sent) != 2 {
		t.Fatalf("expected 2 replies, got %d", len(fa.sent))
	}
	// Replies must carry the originating chat id and echoed text.
	got := map[string]string{}
	for _, s := range fa.sent {
		got[s.ChatID] = s.Text
	}
	if got["1"] != "echo:hello" || got["2"] != "echo:world" {
		t.Errorf("reply routing wrong: %v", got)
	}
}

func TestRunner_NoAdapters(t *testing.T) {
	r := NewRunner(nil, func(context.Context, string, string) (string, error) { return "", nil }, zap.NewNop(), 1)
	if err := r.Run(context.Background()); err == nil {
		t.Error("expected error with no adapters")
	}
}

func TestRunner_NoAgent(t *testing.T) {
	r := NewRunner([]Adapter{&fakeAdapter{name: "x"}}, nil, zap.NewNop(), 1)
	if err := r.Run(context.Background()); err == nil {
		t.Error("expected error with no agent func")
	}
}

func TestSessionKey(t *testing.T) {
	m := InboundMessage{Platform: "telegram", ChatID: "42"}
	if m.SessionKey() != "telegram:42" {
		t.Errorf("got %q", m.SessionKey())
	}
}
