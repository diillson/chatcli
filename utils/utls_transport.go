/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// chromeTLSTransport is an http.RoundTripper that uses uTLS to mimic
// Chrome's TLS fingerprint with proper HTTP/2 support.
//
// Go's http.Transport ignores HTTP/2 when DialTLSContext is set (it only
// enables h2 with the standard crypto/tls package). Since Chrome's TLS
// ClientHello includes "h2" in ALPN, servers like chatgpt.com (Cloudflare)
// negotiate HTTP/2 — but http.Transport tries to speak HTTP/1.1, causing
// "malformed HTTP response" errors (HTTP/2 SETTINGS frames misread as HTTP/1).
//
// This transport checks the ALPN result after the uTLS handshake and routes
// to http2.Transport when "h2" is negotiated.
type chromeTLSTransport struct {
	mu      sync.Mutex
	h2Conns map[string]*http2.ClientConn
}

// NewChromeTLSTransport creates an http.RoundTripper that uses a Chrome-like
// TLS fingerprint via uTLS with automatic HTTP/1.1 / HTTP/2 support based on
// ALPN negotiation.
func NewChromeTLSTransport() http.RoundTripper {
	return &chromeTLSTransport{
		h2Conns: make(map[string]*http2.ClientConn),
	}
}

// dialUTLS performs a raw TCP dial followed by a uTLS handshake that mimics
// Chrome's TLS fingerprint.
func (*chromeTLSTransport) dialUTLS(ctx context.Context, addr string) (*utls.UConn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	rawConn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	uConn := utls.UClient(rawConn, &utls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	}, utls.HelloChrome_Auto)

	if err := uConn.HandshakeContext(ctx); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("utls handshake with %s: %w", addr, err)
	}

	return uConn, nil
}

func (c *chromeTLSTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	addr := req.URL.Host
	if req.URL.Port() == "" {
		addr += ":443"
	}

	// Fast path: reuse a cached HTTP/2 client connection.
	c.mu.Lock()
	cc := c.h2Conns[addr]
	c.mu.Unlock()

	if cc != nil {
		if cc.CanTakeNewRequest() {
			resp, err := cc.RoundTrip(req)
			if err == nil {
				return resp, nil
			}
		}
		// Connection is stale or broken — evict it.
		c.mu.Lock()
		if c.h2Conns[addr] == cc {
			delete(c.h2Conns, addr)
		}
		c.mu.Unlock()
	}

	// Dial a new uTLS connection and check negotiated ALPN protocol.
	uConn, err := c.dialUTLS(req.Context(), addr)
	if err != nil {
		return nil, err
	}

	alpn := uConn.ConnectionState().NegotiatedProtocol

	if alpn == "h2" {
		// Server negotiated HTTP/2 — use http2.Transport.
		newCC, err := (&http2.Transport{}).NewClientConn(uConn)
		if err != nil {
			_ = uConn.Close()
			return nil, fmt.Errorf("h2 client conn to %s: %w", addr, err)
		}
		c.mu.Lock()
		c.h2Conns[addr] = newCC
		c.mu.Unlock()
		return newCC.RoundTrip(req)
	}

	// HTTP/1.1 fallback: wrap the pre-dialed connection in a one-shot
	// http.Transport so it gets used for this single request.
	h1 := &http.Transport{
		DialTLSContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return uConn, nil
		},
	}
	return h1.RoundTrip(req)
}
