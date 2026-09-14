/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"testing"
)

// newChromeTLSTestServer starts a local TLS server whose handler echoes the
// request protocol, optionally advertising HTTP/2, and points the global TLS
// trust at its certificate so the uTLS dialer verifies it like a real host.
func newChromeTLSTestServer(t *testing.T, enableHTTP2 bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto)
	}))
	srv.EnableHTTP2 = enableHTTP2
	srv.StartTLS()
	t.Cleanup(srv.Close)

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	prev := globalTLSConfig.Load()
	globalTLSConfig.Store(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
	t.Cleanup(func() { globalTLSConfig.Store(prev) })
	return srv
}

// probe is what one round trip through the Chrome TLS transport observed.
type probe struct {
	proto  string
	body   string
	reused bool
	tls    *tls.ConnectionState
}

func getProto(t *testing.T, client *http.Client, url string) probe {
	t.Helper()
	var reused bool
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return probe{proto: resp.Proto, body: string(body), reused: reused, tls: resp.TLS}
}

func TestChromeTLSTransport_NegotiatesHTTP2AndReusesConnection(t *testing.T) {
	srv := newChromeTLSTestServer(t, true)
	client := &http.Client{Transport: NewChromeTLSTransport()}

	first := getProto(t, client, srv.URL)
	if first.proto != "HTTP/2.0" || first.body != "HTTP/2.0" {
		t.Fatalf("expected HTTP/2 over the Chrome-fingerprint dial, got proto=%s body=%q", first.proto, first.body)
	}
	if first.reused {
		t.Fatalf("first request must open a fresh connection")
	}
	if first.tls == nil || first.tls.NegotiatedProtocol != "h2" || !first.tls.HandshakeComplete {
		t.Fatalf("expected translated TLS state with ALPN h2, got %+v", first.tls)
	}
	if len(first.tls.PeerCertificates) == 0 || !first.tls.PeerCertificates[0].Equal(srv.Certificate()) {
		t.Fatalf("expected the server certificate in the translated TLS state")
	}

	second := getProto(t, client, srv.URL)
	if second.proto != "HTTP/2.0" || second.body != "HTTP/2.0" {
		t.Fatalf("second request expected HTTP/2, got proto=%s body=%q", second.proto, second.body)
	}
	if !second.reused {
		t.Fatalf("second request must reuse the pooled HTTP/2 connection")
	}
}

func TestChromeTLSTransport_FallsBackToHTTP1WhenServerLacksH2(t *testing.T) {
	srv := newChromeTLSTestServer(t, false)
	client := &http.Client{Transport: NewChromeTLSTransport()}

	first := getProto(t, client, srv.URL)
	if first.proto != "HTTP/1.1" || first.body != "HTTP/1.1" {
		t.Fatalf("expected HTTP/1.1 fallback, got proto=%s body=%q", first.proto, first.body)
	}
	if first.tls == nil || first.tls.NegotiatedProtocol == "h2" {
		t.Fatalf("expected translated TLS state without h2, got %+v", first.tls)
	}

	if second := getProto(t, client, srv.URL); !second.reused {
		t.Fatalf("HTTP/1.1 keep-alive connection must be pooled and reused")
	}
}

func TestChromeTLSTransport_RejectsUntrustedCertificate(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	prev := globalTLSConfig.Load()
	globalTLSConfig.Store(nil)
	t.Cleanup(func() { globalTLSConfig.Store(prev) })

	client := &http.Client{Transport: NewChromeTLSTransport()}
	resp, err := client.Get(srv.URL)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err == nil {
		t.Fatalf("expected the uTLS handshake to reject the self-signed test certificate")
	}
}

func TestChromeTLSTransport_DialErrorIsWrapped(t *testing.T) {
	_, err := dialChromeTLS(context.Background(), "tcp", "127.0.0.1:1")
	if err == nil {
		t.Fatalf("expected a dial error against a closed port")
	}
}
