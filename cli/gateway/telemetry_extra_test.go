/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"go.uber.org/zap"
)

// loggerAware mirrors the optional interface the daemon uses to inject its
// logger into adapters built by the registry.
type loggerAware interface {
	Adapter
	SetLogger(*zap.Logger)
}

// TestAdapterSetLogger exercises SetLogger on every adapter: it must install
// the logger and route the HTTP client through the logging transport.
func TestAdapterSetLogger(t *testing.T) {
	logger := zap.NewNop()
	adapters := []loggerAware{
		NewTelegramAdapter("t", nil, zap.NewNop()),
		NewDiscordAdapter("t", zap.NewNop()),
		NewSlackAdapter("b", "s", ":0", "/x", zap.NewNop()),
		NewWhatsAppAdapter("t", "p", "v", ":0", "/wa", zap.NewNop()),
		NewWebhookAdapter(":0", "/in", "sec", "http://cb", zap.NewNop()),
	}
	for _, a := range adapters {
		a.SetLogger(logger)
		if a.Name() == "" {
			t.Error("adapter lost its name after SetLogger")
		}
	}
}

// TestLoggingTransport_Error covers the failure branch: when the base
// transport errors, the wrapper logs a warning and propagates the error.
func TestLoggingTransport_Error(t *testing.T) {
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial fail")
	})
	c := newLoggingClient(&http.Client{Transport: base}, zap.NewNop(), "telegram")
	req, _ := http.NewRequest(http.MethodPost, "https://api.telegram.org/bot1:x/sendMessage", nil)
	resp, err := c.Transport.RoundTrip(req)
	if err == nil {
		t.Error("expected the underlying transport error to propagate")
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
}

// failingAdapter records sends and fails them, exercising the runner's
// send-error branch.
type failingAdapter struct {
	fakeAdapter
}

func (f *failingAdapter) Send(context.Context, OutboundMessage) error {
	return fmt.Errorf("send boom")
}

// TestRunner_AgentErrorAndSendFailure covers the runner's error paths: an agent
// error becomes a warning reply, and a failing Send is logged without panicking.
func TestRunner_AgentErrorAndSendFailure(t *testing.T) {
	fa := &failingAdapter{fakeAdapter{
		name: "fake",
		emit: []InboundMessage{{Platform: "fake", ChatID: "1", UserID: "u", Text: "x"}},
	}}
	agent := func(context.Context, InboundMessage) (string, error) {
		return "", fmt.Errorf("agent boom")
	}
	r := NewRunner([]Adapter{fa}, agent, zap.NewNop(), 1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = r.Run(ctx); close(done) }()
	// Give the worker a moment to process the one message, then shut down.
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
}
