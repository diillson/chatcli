package bus

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublishSubscribe(t *testing.T) {
	mb := New(64)
	defer mb.Close()

	received := make(chan InboundMessage, 1)
	mb.Subscribe(Subscription{
		Handler: func(msg interface{}) {
			if m, ok := msg.(InboundMessage); ok {
				received <- m
			}
		},
	})

	err := mb.PublishInbound(context.Background(), InboundMessage{
		Channel: "cli",
		Content: "hello",
		Type:    MessageTypeChat,
	})
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	select {
	case msg := <-received:
		if msg.Content != "hello" {
			t.Errorf("expected 'hello', got %q", msg.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestTypedSubscription(t *testing.T) {
	mb := New(64)
	defer mb.Close()

	toolType := MessageTypeToolCall
	var count atomic.Int32

	mb.Subscribe(Subscription{
		MessageType: &toolType,
		Handler: func(msg interface{}) {
			count.Add(1)
		},
	})

	// Publish non-matching type
	_ = mb.PublishInbound(context.Background(), InboundMessage{Type: MessageTypeChat, Content: "x"})
	// Publish matching type
	_ = mb.PublishInbound(context.Background(), InboundMessage{Type: MessageTypeToolCall, Content: "y"})

	time.Sleep(100 * time.Millisecond)
	if count.Load() != 1 {
		t.Errorf("expected 1 delivery, got %d", count.Load())
	}
}

func TestChannelFilter(t *testing.T) {
	mb := New(64)
	defer mb.Close()

	var count atomic.Int32
	mb.Subscribe(Subscription{
		Channel: "grpc",
		Handler: func(msg interface{}) {
			count.Add(1)
		},
	})

	_ = mb.PublishInbound(context.Background(), InboundMessage{Channel: "cli", Content: "x"})
	_ = mb.PublishInbound(context.Background(), InboundMessage{Channel: "grpc", Content: "y"})

	time.Sleep(100 * time.Millisecond)
	if count.Load() != 1 {
		t.Errorf("expected 1 delivery, got %d", count.Load())
	}
}

func TestUnsubscribe(t *testing.T) {
	mb := New(64)
	defer mb.Close()

	var count atomic.Int32
	id := mb.Subscribe(Subscription{
		Handler: func(msg interface{}) {
			count.Add(1)
		},
	})

	_ = mb.PublishInbound(context.Background(), InboundMessage{Content: "1"})
	time.Sleep(50 * time.Millisecond)

	mb.Unsubscribe(id)
	_ = mb.PublishInbound(context.Background(), InboundMessage{Content: "2"})
	time.Sleep(50 * time.Millisecond)

	if count.Load() != 1 {
		t.Errorf("expected 1 delivery after unsubscribe, got %d", count.Load())
	}
}

func TestClose(t *testing.T) {
	mb := New(64)
	mb.Close()

	err := mb.PublishInbound(context.Background(), InboundMessage{Content: "x"})
	if err != ErrBusClosed {
		t.Errorf("expected ErrBusClosed, got %v", err)
	}
}

func TestRequestReply(t *testing.T) {
	mb := New(64)
	defer mb.Close()

	// Subscribe a handler that replies to inbound messages
	mb.Subscribe(Subscription{
		Handler: func(msg interface{}) {
			if m, ok := msg.(InboundMessage); ok {
				_ = mb.PublishOutbound(context.Background(), OutboundMessage{
					ReplyToID: m.ID,
					Content:   "reply:" + m.Content,
				})
			}
		},
	})

	reply, err := mb.Request(context.Background(), InboundMessage{Content: "ping"}, time.Second)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if reply.Content != "reply:ping" {
		t.Errorf("expected 'reply:ping', got %q", reply.Content)
	}
}

func TestRequestReplyTimeout(t *testing.T) {
	mb := New(64)
	defer mb.Close()

	// No handler — should timeout
	_, err := mb.Request(context.Background(), InboundMessage{Content: "ping"}, 50*time.Millisecond)
	if err != ErrTimeout {
		t.Errorf("expected ErrTimeout, got %v", err)
	}
}

func TestMetrics(t *testing.T) {
	mb := New(64)
	defer mb.Close()

	mb.Subscribe(Subscription{
		Handler: func(msg interface{}) {},
	})

	_ = mb.PublishInbound(context.Background(), InboundMessage{Content: "1"})
	_ = mb.PublishInbound(context.Background(), InboundMessage{Content: "2"})

	time.Sleep(100 * time.Millisecond)
	m := mb.GetMetrics()
	if m.Published != 2 {
		t.Errorf("expected 2 published, got %d", m.Published)
	}
	if m.Delivered < 2 {
		t.Errorf("expected at least 2 delivered, got %d", m.Delivered)
	}
}

func TestConcurrency(t *testing.T) {
	mb := New(256)
	defer mb.Close()

	var delivered atomic.Int64
	mb.Subscribe(Subscription{
		Handler: func(msg interface{}) {
			delivered.Add(1)
		},
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = mb.PublishInbound(context.Background(), InboundMessage{Content: "msg"})
		}(i)
	}
	wg.Wait()
	time.Sleep(200 * time.Millisecond)

	if delivered.Load() != 100 {
		t.Errorf("expected 100 delivered, got %d", delivered.Load())
	}
}

func TestContextCancelled(t *testing.T) {
	mb := New(64)
	defer mb.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := mb.PublishInbound(ctx, InboundMessage{Content: "x"})
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}
