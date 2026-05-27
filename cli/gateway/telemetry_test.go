/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"net/http"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestSanitizeAPIPath(t *testing.T) {
	cases := map[string]string{
		"/bot123456:AAEjklToken/sendMessage": "/bot***/sendMessage",
		"/bot123456:AAEjklToken/getUpdates":  "/bot***/getUpdates",
		"/bot123456:AAEjklToken":             "/bot***", // no trailing segment
		"/v1/messages":                       "/v1/messages",
		"":                                   "",
	}
	for in, want := range cases {
		if got := sanitizeAPIPath(in); got != want {
			t.Errorf("sanitizeAPIPath(%q) = %q, want %q", in, got, want)
		}
		if in != "" && want != in {
			// The token must never survive sanitization.
			if got := sanitizeAPIPath(in); got == in {
				t.Errorf("token leaked through sanitizeAPIPath(%q)", in)
			}
		}
	}
}

func TestIsPollPath(t *testing.T) {
	if !isPollPath("/bot123/getUpdates") {
		t.Error("getUpdates should be detected as a poll path")
	}
	if isPollPath("/bot123/sendMessage") {
		t.Error("sendMessage is not a poll path")
	}
}

func TestNewLoggingClient_NilLoggerNoop(t *testing.T) {
	c := &http.Client{}
	if got := newLoggingClient(c, nil, "telegram"); got != c || got.Transport != nil {
		t.Error("nil logger must leave the client untouched")
	}
}

// roundTripFunc adapts a func to http.RoundTripper for testing.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLoggingTransport_LogsAndSanitizes(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	logger := zap.New(core)

	base := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})
	c := &http.Client{Transport: base}
	c = newLoggingClient(c, logger, "telegram")

	// Double-wrap must not nest transports.
	c = newLoggingClient(c, logger, "telegram")
	if lt, ok := c.Transport.(*loggingTransport); !ok {
		t.Fatal("transport should be a loggingTransport")
	} else if _, nested := lt.base.(*loggingTransport); nested {
		t.Error("double-wrap nested the logging transport")
	}

	// A sendMessage call logs at Info with the token stripped from the path.
	req, _ := http.NewRequest(http.MethodPost, "https://api.telegram.org/bot123:SECRET/sendMessage", nil)
	if _, err := c.Transport.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	entries := logs.FilterMessage("gateway: external request").All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 external-request log, got %d", len(entries))
	}
	for _, f := range entries[0].Context {
		if f.Key == "path" {
			if f.String != "/bot***/sendMessage" {
				t.Errorf("path field = %q, want sanitized", f.String)
			}
		}
	}

	// A getUpdates poll logs at Debug (filtered out by the Info-level observer).
	pollReq, _ := http.NewRequest(http.MethodGet, "https://api.telegram.org/bot123:SECRET/getUpdates", nil)
	if _, err := c.Transport.RoundTrip(pollReq); err != nil {
		t.Fatalf("RoundTrip poll: %v", err)
	}
	if n := logs.FilterMessage("gateway: external poll").Len(); n != 0 {
		t.Errorf("poll should log at Debug (suppressed at Info), got %d Info entries", n)
	}
}
