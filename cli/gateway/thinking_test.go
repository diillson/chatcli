/*
 * ChatCLI - tests for the "assistant is working" thinking indicator.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// stubAdapter is a no-op Adapter for driving startThinking directly.
type stubAdapter struct{}

func (stubAdapter) Name() string                                       { return "stub" }
func (stubAdapter) Start(context.Context, chan<- InboundMessage) error { return nil }
func (stubAdapter) Send(context.Context, OutboundMessage) error        { return nil }

// typingStub adds the native typing capability and counts calls.
type typingStub struct {
	stubAdapter
	n int32
}

func (t *typingStub) SendTyping(context.Context, string) error {
	atomic.AddInt32(&t.n, 1)
	return nil
}

func TestStartThinking_NativeTyping(t *testing.T) {
	ts := &typingStub{}
	r := &Runner{logger: zap.NewNop(), typingRefresh: 5 * time.Millisecond}
	stop := r.startThinking(context.Background(), ts, InboundMessage{ChatID: "1"}, func(string, string) {
		t.Error("native typing must not send a text notice")
	})
	time.Sleep(30 * time.Millisecond)
	stop()
	if atomic.LoadInt32(&ts.n) < 1 {
		t.Error("expected at least one SendTyping call")
	}
}

func TestStartThinking_TextFallback(t *testing.T) {
	var mu sync.Mutex
	var got string
	r := &Runner{logger: zap.NewNop(), thinkingDelay: 10 * time.Millisecond}
	r.SetThinkingNotice("working…")
	stop := r.startThinking(context.Background(), stubAdapter{}, InboundMessage{ChatID: "1"}, func(kind, text string) {
		mu.Lock()
		got = kind + ":" + text
		mu.Unlock()
	})
	time.Sleep(40 * time.Millisecond)
	stop()
	mu.Lock()
	defer mu.Unlock()
	if got != "thinking:working…" {
		t.Errorf("expected a delayed text notice, got %q", got)
	}
}

func TestStartThinking_FastReplyNoNotice(t *testing.T) {
	r := &Runner{logger: zap.NewNop(), thinkingDelay: time.Second}
	stop := r.startThinking(context.Background(), stubAdapter{}, InboundMessage{ChatID: "1"}, func(string, string) {
		t.Error("a reply faster than thinkingDelay must send no notice")
	})
	stop() // agent returned immediately
	time.Sleep(20 * time.Millisecond)
}

func TestTelegramSendTyping(t *testing.T) {
	var gotAction string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotAction = string(body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	ad := NewTelegramAdapter("TOK", nil, zap.NewNop())
	ad.baseURL = srv.URL
	if err := ad.SendTyping(context.Background(), "42"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotAction, "typing") {
		t.Errorf("payload missing typing action: %q", gotAction)
	}
}
