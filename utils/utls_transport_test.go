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

func getProto(t *testing.T, client *http.Client, url string) (*http.Response, string, bool) {
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
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, string(body), reused
}

func TestChromeTLSTransport_NegotiatesHTTP2AndReusesConnection(t *testing.T) {
	srv := newChromeTLSTestServer(t, true)
	client := &http.Client{Transport: NewChromeTLSTransport()}

	resp, body, reused := getProto(t, client, srv.URL)
	if resp.ProtoMajor != 2 || body != "HTTP/2.0" {
		t.Fatalf("expected HTTP/2 over the Chrome-fingerprint dial, got proto=%s body=%q", resp.Proto, body)
	}
	if reused {
		t.Fatalf("first request must open a fresh connection")
	}
	if resp.TLS == nil || resp.TLS.NegotiatedProtocol != "h2" || !resp.TLS.HandshakeComplete {
		t.Fatalf("expected translated TLS state with ALPN h2, got %+v", resp.TLS)
	}
	if len(resp.TLS.PeerCertificates) == 0 || !resp.TLS.PeerCertificates[0].Equal(srv.Certificate()) {
		t.Fatalf("expected the server certificate in the translated TLS state")
	}

	resp2, body2, reused2 := getProto(t, client, srv.URL)
	if resp2.ProtoMajor != 2 || body2 != "HTTP/2.0" {
		t.Fatalf("second request expected HTTP/2, got proto=%s body=%q", resp2.Proto, body2)
	}
	if !reused2 {
		t.Fatalf("second request must reuse the pooled HTTP/2 connection")
	}
}

func TestChromeTLSTransport_FallsBackToHTTP1WhenServerLacksH2(t *testing.T) {
	srv := newChromeTLSTestServer(t, false)
	client := &http.Client{Transport: NewChromeTLSTransport()}

	resp, body, _ := getProto(t, client, srv.URL)
	if resp.ProtoMajor != 1 || body != "HTTP/1.1" {
		t.Fatalf("expected HTTP/1.1 fallback, got proto=%s body=%q", resp.Proto, body)
	}
	if resp.TLS == nil || resp.TLS.NegotiatedProtocol == "h2" {
		t.Fatalf("expected translated TLS state without h2, got %+v", resp.TLS)
	}

	_, _, reused := getProto(t, client, srv.URL)
	if !reused {
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
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("expected the uTLS handshake to reject the self-signed test certificate")
	}
}

func TestChromeTLSTransport_DialErrorIsWrapped(t *testing.T) {
	_, err := dialChromeTLS(context.Background(), "tcp", "127.0.0.1:1")
	if err == nil {
		t.Fatalf("expected a dial error against a closed port")
	}
}
