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
	"time"

	utls "github.com/refraction-networking/utls"
)

// chromeTLSDialTimeout bounds the TCP connect of the Chrome-fingerprint
// dialer; the TLS handshake itself is bounded by the request context.
const chromeTLSDialTimeout = 10 * time.Second

// chromeTLSIdleConnTimeout mirrors http.DefaultTransport so idle
// fingerprinted connections are recycled instead of lingering forever.
const chromeTLSIdleConnTimeout = 90 * time.Second

// NewChromeTLSTransport creates an http.RoundTripper that uses a Chrome-like
// TLS fingerprint via uTLS with automatic HTTP/1.1 / HTTP/2 support based on
// ALPN negotiation.
//
// The transport is a standard http.Transport whose TLS dial is delegated to
// uTLS. http.Transport probes the dialed connection for a ConnectionState
// method returning crypto/tls's ConnectionState; uTLS exposes its own type,
// so the connection is wrapped in chromeTLSConn to translate it. With that
// translation in place the standard library sees the negotiated ALPN
// protocol, routes "h2" connections to its bundled HTTP/2 client and keeps
// HTTP/1.1 for everything else, with regular connection pooling for both.
func NewChromeTLSTransport() http.RoundTripper {
	return &http.Transport{
		DialTLSContext:    dialChromeTLS,
		ForceAttemptHTTP2: true,
		IdleConnTimeout:   chromeTLSIdleConnTimeout,
	}
}

// dialChromeTLS performs a raw TCP dial followed by a uTLS handshake that
// mimics Chrome's TLS fingerprint, returning the connection adapted for
// http.Transport.
func dialChromeTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	rawConn, err := (&net.Dialer{Timeout: chromeTLSDialTimeout}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	uCfg := &utls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	}
	// Propagate the corporate TLS trust overrides — uTLS has its own
	// handshake and does not see http.Transport's TLSClientConfig.
	if g := GlobalTLSConfig(); g != nil {
		uCfg.RootCAs = g.RootCAs
		uCfg.InsecureSkipVerify = g.InsecureSkipVerify // #nosec G402 -- mirrors the documented global opt-in
	}
	uConn := utls.UClient(rawConn, uCfg, utls.HelloChrome_Auto)

	if err := uConn.HandshakeContext(ctx); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("utls handshake with %s: %w", addr, err)
	}

	return &chromeTLSConn{UConn: uConn}, nil
}

// chromeTLSConn adapts a uTLS connection to the shape http.Transport and
// its bundled HTTP/2 client probe on a DialTLSContext result: a
// ConnectionState method returning crypto/tls's ConnectionState. Without
// it the transport cannot see that the server agreed on "h2" and would
// speak HTTP/1.1 into an HTTP/2 connection ("malformed HTTP response").
type chromeTLSConn struct {
	*utls.UConn
}

// ConnectionState translates the uTLS handshake state into the standard
// library's type. Every field that exists on both sides is carried over so
// http.Response.TLS is populated as it would be for a crypto/tls dial.
func (c *chromeTLSConn) ConnectionState() tls.ConnectionState {
	s := c.UConn.ConnectionState()
	return tls.ConnectionState{
		Version:                     s.Version,
		HandshakeComplete:           s.HandshakeComplete,
		DidResume:                   s.DidResume,
		CipherSuite:                 s.CipherSuite,
		NegotiatedProtocol:          s.NegotiatedProtocol,
		ServerName:                  s.ServerName,
		PeerCertificates:            s.PeerCertificates,
		VerifiedChains:              s.VerifiedChains,
		SignedCertificateTimestamps: s.SignedCertificateTimestamps,
		OCSPResponse:                s.OCSPResponse,
	}
}
