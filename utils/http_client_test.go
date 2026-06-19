/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	"go.uber.org/zap"
)

// NewHTTPClientH1 must pin HTTP/1.1: ForceAttemptHTTP2 off, ALPN restricted to
// http/1.1, and TLSNextProto a non-nil empty map (the canonical h2-disable).
// This is the fix for the intermittent "unexpected EOF" Go's h2 client returns
// on POST-with-body to some Cloudflare-fronted hosts (api.openai.com, api.x.ai).
func TestNewHTTPClientH1_DisablesHTTP2(t *testing.T) {
	c := NewHTTPClientH1(zap.NewNop(), 5*time.Second)
	lt, ok := c.Transport.(*LoggingTransport)
	if !ok {
		t.Fatalf("expected *LoggingTransport, got %T", c.Transport)
	}
	tr, ok := lt.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport inner, got %T", lt.Transport)
	}
	if tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 must be false")
	}
	if tr.TLSNextProto == nil || len(tr.TLSNextProto) != 0 {
		t.Errorf("TLSNextProto must be a non-nil empty map, got %v", tr.TLSNextProto)
	}
	if tr.TLSClientConfig == nil || len(tr.TLSClientConfig.NextProtos) != 1 || tr.TLSClientConfig.NextProtos[0] != "http/1.1" {
		t.Errorf("TLSClientConfig.NextProtos must be [http/1.1], got %v", tr.TLSClientConfig)
	}
	if tr.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Errorf("MinVersion must be >= TLS 1.2, got %x", tr.TLSClientConfig.MinVersion)
	}
}
