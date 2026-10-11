/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCheckIPRefusesNonPublic(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.8.8.8", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "fe80::1", "fd00:ec2::254", "0.0.0.0", "::",
		"100.100.100.200", "224.0.0.1", "255.255.255.255", "::ffff:127.0.0.1",
		"::ffff:169.254.169.254", "64:ff9b::a9fe:a9fe", "2002:7f00:1::1",
	}
	for _, s := range blocked {
		if err := CheckIP(net.ParseIP(s)); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s must be refused, got %v", s, err)
		}
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if err := CheckIP(net.ParseIP(s)); err != nil {
			t.Errorf("%s is public, got %v", s, err)
		}
	}
	if CheckIP(nil) == nil {
		t.Error("an unparsable address must be refused")
	}
}

func TestCheckURL(t *testing.T) {
	refused := []string{
		"http://example.com/a.png",
		"https://127.0.0.1/a.png",
		"https://169.254.169.254/latest/meta-data/",
		"https://[::1]/x",
		"https://localhost/x",
		"https://metadata.google.internal/computeMetadata/v1/",
		"https://user:pass@example.com/x",
		"file:///etc/passwd",
		"https:///nohost",
		"gopher://example.com",
	}
	for _, raw := range refused {
		if _, err := CheckURL(raw); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s must be refused, got %v", raw, err)
		}
	}
	if _, err := CheckURL("https://cdn.example.com/a.png"); err != nil {
		t.Errorf("a public https URL must pass: %v", err)
	}
}

// The dial-time hook is what stops a hostname that resolves to a private
// address; it sees the concrete IP, never the name.
func TestDialControlRefusesResolvedPrivateAddresses(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:443", "[::1]:443", "169.254.169.254:80", "10.0.0.5:443", "localhost:443"} {
		if err := dialControl("tcp", addr, nil); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s must be refused at dial time, got %v", addr, err)
		}
	}
	if err := dialControl("tcp", "93.184.216.34:443", nil); err != nil {
		t.Errorf("a public address must be dialable: %v", err)
	}
}

// A redirect hop to an internal target is refused even when the first URL
// was public.
func TestCheckRedirectReValidatesEveryHop(t *testing.T) {
	first, _ := http.NewRequest(http.MethodGet, "https://cdn.example.com/a", nil)
	hop, _ := http.NewRequest(http.MethodGet, "https://169.254.169.254/latest/", nil)
	if err := checkRedirect(hop, []*http.Request{first}); !errors.Is(err, ErrBlocked) {
		t.Errorf("redirect to metadata must be refused, got %v", err)
	}
	plain, _ := http.NewRequest(http.MethodGet, "http://cdn.example.com/b", nil)
	if err := checkRedirect(plain, []*http.Request{first}); !errors.Is(err, ErrBlocked) {
		t.Errorf("a downgrade to http must be refused, got %v", err)
	}
	other, _ := http.NewRequest(http.MethodGet, "https://other.example.com/b", nil)
	other.Header.Set("Authorization", "Bearer x")
	if err := checkRedirect(other, []*http.Request{first}); err != nil {
		t.Fatalf("public hop refused: %v", err)
	}
	if other.Header.Get("Authorization") != "" {
		t.Error("credentials must not follow a cross-host redirect")
	}
	via := make([]*http.Request, maxRedirects)
	for i := range via {
		via[i] = first
	}
	if err := checkRedirect(other, via); err == nil {
		t.Error("the redirect chain must be capped")
	}
}

// End to end: the client refuses a loopback server even when it is reached
// through a hostname, and the refusal happens before any byte is sent.
func TestClientRefusesLoopbackServer(t *testing.T) {
	hit := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hit = true }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	c := NewClient(5 * time.Second)
	for _, target := range []string{srv.URL, "https://localhost:" + u.Port() + "/"} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
		resp, err := c.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			t.Fatalf("%s: a loopback server must be unreachable", target)
		}
		if !errors.Is(err, ErrBlocked) && !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("%s: refusal should come from the guard, got %v", target, err)
		}
	}
	if hit {
		t.Error("the server must never see the request")
	}
}
