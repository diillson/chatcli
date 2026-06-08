/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateWebTarget_SchemeAndHost(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "")
	cases := []struct {
		name    string
		url     string
		wantErr string // substring; "" means must succeed
	}{
		{"https ok", "https://example.com/path", ""},
		{"http ok", "http://example.com", ""},
		{"file blocked", "file:///etc/passwd", "scheme"},
		{"gopher blocked", "gopher://evil/", "scheme"},
		{"ftp blocked", "ftp://host/", "scheme"},
		{"no host", "https://", "no host"},
		{"metadata hostname", "http://metadata.google.internal/computeMetadata/v1/", "metadata"},
		{"instance-data", "http://instance-data/latest/", "metadata"},
		{"literal metadata ip", "http://169.254.169.254/latest/meta-data/", "metadata/link-local"},
		{"public literal ip", "http://1.1.1.1/", ""},
		{"private literal ip default-allowed", "http://10.0.0.5/metrics", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateWebTarget(tc.url)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateWebTarget_BlockPrivateOptIn(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "true")
	for _, raw := range []string{"http://10.0.0.5/", "http://127.0.0.1/", "http://192.168.1.1/"} {
		if _, err := validateWebTarget(raw); err == nil {
			t.Fatalf("expected %s to be blocked when BLOCK_PRIVATE=true", raw)
		}
	}
	// Public still allowed.
	if _, err := validateWebTarget("http://1.1.1.1/"); err != nil {
		t.Fatalf("public IP must remain allowed: %v", err)
	}
	// Metadata still blocked regardless.
	if _, err := validateWebTarget("http://169.254.169.254/"); err == nil {
		t.Fatal("metadata must always be blocked")
	}
}

func TestSSRFDialControl(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "")
	// Link-local/metadata is always refused at dial time.
	if err := ssrfDialControl("tcp", "169.254.169.254:80", nil); err == nil {
		t.Fatal("dial control must block metadata IP")
	}
	// Public allowed.
	if err := ssrfDialControl("tcp", "1.1.1.1:443", nil); err != nil {
		t.Fatalf("dial control must allow public IP: %v", err)
	}
	// Private allowed by default.
	if err := ssrfDialControl("tcp", "10.0.0.5:80", nil); err != nil {
		t.Fatalf("private must be allowed by default: %v", err)
	}
}

func TestSSRFDialControl_BlockPrivate(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "true")
	for _, addr := range []string{"10.0.0.5:80", "127.0.0.1:8080", "192.168.0.2:443"} {
		if err := ssrfDialControl("tcp", addr, nil); err == nil {
			t.Fatalf("dial control must block %s when BLOCK_PRIVATE=true", addr)
		}
	}
}

func TestCheckWebIP_IPv6(t *testing.T) {
	// IPv6 link-local must be blocked always.
	if err := checkWebIP(net.ParseIP("fe80::1"), false, "h"); err == nil {
		t.Fatal("IPv6 link-local must be blocked")
	}
	// IPv4-mapped metadata must be blocked.
	if err := checkWebIP(net.ParseIP("::ffff:169.254.169.254"), false, "h"); err == nil {
		t.Fatal("IPv4-mapped metadata must be blocked")
	}
	// IPv6 ULA blocked only when private is blocked.
	if err := checkWebIP(net.ParseIP("fc00::1"), false, "h"); err != nil {
		t.Fatalf("ULA allowed by default: %v", err)
	}
	if err := checkWebIP(net.ParseIP("fc00::1"), true, "h"); err == nil {
		t.Fatal("ULA must be blocked when private blocked")
	}
}

// validateRedirect must refuse a hop to a metadata/internal URL and cap chains.
func TestValidateRedirect(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "")
	mkReq := func(u string) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, u, nil)
		return r
	}
	orig := mkReq("https://example.com/")

	// Redirect to metadata → refused.
	if err := validateRedirect(mkReq("http://169.254.169.254/"), []*http.Request{orig}); err == nil {
		t.Fatal("redirect to metadata must be refused")
	}
	// Redirect to file scheme → refused.
	if err := validateRedirect(mkReq("file:///etc/passwd"), []*http.Request{orig}); err == nil {
		t.Fatal("redirect to file scheme must be refused")
	}
	// Normal redirect → allowed, and cross-host strips auth.
	next := mkReq("https://other.example/")
	next.Header.Set("Authorization", "Bearer secret")
	if err := validateRedirect(next, []*http.Request{orig}); err != nil {
		t.Fatalf("normal redirect should be allowed: %v", err)
	}
	if next.Header.Get("Authorization") != "" {
		t.Fatal("Authorization must be stripped on cross-host redirect")
	}
	// Too many hops → refused.
	chain := make([]*http.Request, maxWebRedirects)
	for i := range chain {
		chain[i] = orig
	}
	if err := validateRedirect(mkReq("https://example.com/"), chain); err == nil {
		t.Fatal("redirect chain over the cap must be refused")
	}
}

// End-to-end: the shared client must refuse to follow a redirect that points at
// the cloud metadata endpoint — the canonical SSRF bypass.
func TestWebHTTPClient_RefusesRedirectToMetadata(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "")

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer redirector.Close()

	resp, err := webHTTPClient().Get(redirector.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected the client to refuse the metadata redirect")
	}
	if !strings.Contains(err.Error(), "metadata") && !strings.Contains(err.Error(), "refusing redirect") {
		t.Fatalf("error should mention the blocked redirect, got: %v", err)
	}
}

// End-to-end: a direct fetch to a loopback httptest server works by default
// (private allowed), proving the guard doesn't break legitimate internal use.
func TestWebHTTPClient_AllowsLoopbackByDefault(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	resp, err := webHTTPClient().Get(srv.URL)
	if err != nil {
		t.Fatalf("loopback fetch should succeed by default: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
