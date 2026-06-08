/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func basicB64(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

func newProxyReq(t *testing.T, target string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	return req
}

func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "all_proxy", "no_proxy",
		envProxyAuth,
	} {
		t.Setenv(k, "")
	}
}

func TestWebProxyForRequest_NoProxy(t *testing.T) {
	clearProxyEnv(t)
	got, err := webProxyForRequest(newProxyReq(t, "https://example.com"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected no proxy, got %v", got)
	}
}

func TestWebProxyForRequest_PlainCreds(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://alice:s3cr3t@proxy.corp:8080")

	got, err := webProxyForRequest(newProxyReq(t, "https://example.com"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Host != "proxy.corp:8080" || got.User == nil {
		t.Fatalf("expected proxy with creds, got %v", got)
	}
	if u := got.User.Username(); u != "alice" {
		t.Fatalf("expected user alice, got %q", u)
	}
	if p, _ := got.User.Password(); p != "s3cr3t" {
		t.Fatalf("expected pass s3cr3t, got %q", p)
	}
}

// THE FIX: credentials that net/url rejects (domain backslash, '%', '#',
// space) must still be recovered so Proxy-Authorization is emitted, instead of
// http.ProxyFromEnvironment silently dropping the proxy.
func TestWebProxyForRequest_TolerantSpecialChars(t *testing.T) {
	cases := []struct {
		name, raw, wantUser, wantPass string
	}{
		{"domain-backslash", `http://CORP\jdoe:s3cr3t@proxy.corp:8080`, `CORP\jdoe`, "s3cr3t"},
		{"percent", "http://alice:pa%ss@proxy.corp:8080", "alice", "pa%ss"},
		{"hash", "http://alice:pa#ss@proxy.corp:8080", "alice", "pa#ss"},
		{"space", "http://alice:pa ss@proxy.corp:8080", "alice", "pa ss"},
		{"at-in-pass", "http://alice:p@ss@proxy.corp:8080", "alice", "p@ss"},
		{"no-scheme", "alice:s3cr3t@proxy.corp:8080", "alice", "s3cr3t"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearProxyEnv(t)
			t.Setenv("HTTPS_PROXY", tc.raw)

			got, err := webProxyForRequest(newProxyReq(t, "https://example.com"))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil || got.User == nil {
				t.Fatalf("expected proxy with recovered creds, got %v", got)
			}
			if u := got.User.Username(); u != tc.wantUser {
				t.Fatalf("user: want %q got %q", tc.wantUser, u)
			}
			if p, _ := got.User.Password(); p != tc.wantPass {
				t.Fatalf("pass: want %q got %q", tc.wantPass, p)
			}
			if got.Host != "proxy.corp:8080" {
				t.Fatalf("host: want proxy.corp:8080 got %q", got.Host)
			}
			// And crucially the re-encoded URL must produce a parseable,
			// credential-bearing URL string (what Go base64-encodes for auth).
			if reparsed, perr := url.Parse(got.String()); perr != nil || reparsed.User == nil {
				t.Fatalf("re-encoded URL not clean: %q err=%v", got.String(), perr)
			}
		})
	}
}

func TestWebProxyForRequest_RespectsNoProxy(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://alice:s3cr3t@proxy.corp:8080")
	t.Setenv("NO_PROXY", "example.com")

	got, err := webProxyForRequest(newProxyReq(t, "https://example.com"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected direct connection for NO_PROXY host, got %v", got)
	}
}

func TestWebProxyForRequest_SchemeSelection(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTP_PROXY", "http://httponly:9090")
	t.Setenv("HTTPS_PROXY", "http://httpsonly:9443")

	httpGot, _ := webProxyForRequest(newProxyReq(t, "http://example.com"))
	if httpGot == nil || httpGot.Host != "httponly:9090" {
		t.Fatalf("http target: want httponly:9090, got %v", httpGot)
	}
	httpsGot, _ := webProxyForRequest(newProxyReq(t, "https://example.com"))
	if httpsGot == nil || httpsGot.Host != "httpsonly:9443" {
		t.Fatalf("https target: want httpsonly:9443, got %v", httpsGot)
	}
}

func TestWebProxyForRequest_AllProxyFallback(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("ALL_PROXY", "http://allproxy:1080")

	got, _ := webProxyForRequest(newProxyReq(t, "https://example.com"))
	if got == nil || got.Host != "allproxy:1080" {
		t.Fatalf("expected ALL_PROXY fallback allproxy:1080, got %v", got)
	}
}

func TestProxyAuthRawHeader(t *testing.T) {
	clearProxyEnv(t)
	if got := proxyAuthRawHeader(); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	t.Setenv(envProxyAuth, "  Negotiate abc  ")
	if got := proxyAuthRawHeader(); got != "Negotiate abc" {
		t.Fatalf("expected trimmed 'Negotiate abc', got %q", got)
	}
}

// End-to-end: a plain-HTTP target routed through a stub proxy must arrive with
// the raw Proxy-Authorization header injected by the RoundTripper wrapper.
func TestProxyAuthTransport_InjectsRawHeaderOnHTTPTarget(t *testing.T) {
	clearProxyEnv(t)

	var gotAuth string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Proxy-Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer proxy.Close()

	t.Setenv("HTTP_PROXY", proxy.URL) // no creds in URL → raw header path
	t.Setenv(envProxyAuth, "Negotiate TOKEN123")

	client := &http.Client{Transport: &proxyAuthTransport{base: newWebTransport()}}
	resp, err := client.Get("http://target.internal/page")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if gotAuth != "Negotiate TOKEN123" {
		t.Fatalf("proxy did not receive injected Proxy-Authorization, got %q", gotAuth)
	}
}

// Basic creds in the proxy URL must reach the proxy too (Go injects them); the
// raw-header wrapper must stay out of the way.
func TestProxyAuthTransport_BasicFromURLOnHTTPTarget(t *testing.T) {
	clearProxyEnv(t)

	var gotAuth string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Proxy-Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	u, _ := url.Parse(proxy.URL)
	t.Setenv("HTTP_PROXY", "http://alice:s3cr3t@"+u.Host)

	client := &http.Client{Transport: &proxyAuthTransport{base: newWebTransport()}}
	resp, err := client.Get("http://target.internal/page")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// alice:s3cr3t -> base64
	if gotAuth != "Basic "+basicB64("alice", "s3cr3t") {
		t.Fatalf("expected Basic auth from URL, got %q", gotAuth)
	}
}

// The wrapper must NOT attach Proxy-Authorization when no proxy applies —
// otherwise we'd leak the corporate credential to origins on a direct call.
func TestProxyAuthTransport_NoLeakWithoutProxy(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv(envProxyAuth, "Negotiate TOKEN123")

	var gotAuth string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Proxy-Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer origin.Close()

	client := &http.Client{Transport: &proxyAuthTransport{base: newWebTransport()}}
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if gotAuth != "" {
		t.Fatalf("Proxy-Authorization leaked on direct connection: %q", gotAuth)
	}
}

// A TLS-intercepting gateway that 401s "browsers" but allows tool UAs must be
// transparently handled: webGet retries once with the neutral UA and succeeds.
func TestWebGet_RetriesNeutralUAOnGatewayChallenge(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "")
	t.Setenv("CHATCLI_WEBFETCH_USER_AGENT", "")

	var seenUAs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua := r.Header.Get("User-Agent")
		seenUAs = append(seenUAs, ua)
		if strings.Contains(ua, "Mozilla") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	resp, err := webGet(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("webGet failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after UA fallback, got %d", resp.StatusCode)
	}
	if len(seenUAs) != 2 {
		t.Fatalf("expected 2 attempts (browser then neutral), got %d: %v", len(seenUAs), seenUAs)
	}
	if strings.Contains(seenUAs[1], "Mozilla") {
		t.Fatalf("retry must not use a browser UA, got %q", seenUAs[1])
	}
}

// An explicit UA override disables the fallback — the user picked it on purpose.
func TestWebGet_NoFallbackWhenUAOverridden(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "")
	t.Setenv("CHATCLI_WEBFETCH_USER_AGENT", "Mozilla/5.0 custom")

	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	resp, err := webGet(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("webGet failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 passthrough, got %d", resp.StatusCode)
	}
	if attempts != 1 {
		t.Fatalf("override must disable retry; expected 1 attempt, got %d", attempts)
	}
}

func TestFallbackUserAgent_NotBrowserLike(t *testing.T) {
	if strings.Contains(fallbackUserAgent, "Mozilla") || strings.Contains(fallbackUserAgent, "Chrome") {
		t.Fatalf("fallback UA must not look like a browser: %q", fallbackUserAgent)
	}
	if !strings.HasPrefix(fallbackUserAgent, "curl/") {
		t.Fatalf("fallback UA should be the curl identity: %q", fallbackUserAgent)
	}
}

func TestWebHTTPClient_Singleton(t *testing.T) {
	first := webHTTPClient()
	if first == nil {
		t.Fatal("expected non-nil client")
	}
	if second := webHTTPClient(); second != first {
		t.Fatal("expected a stable singleton")
	}
}
