/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubWebLookup replaces the resolver used by the render and browser
// guards and clears the verdict cache, so a test controls what a name
// resolves to without the network.
func stubWebLookup(t *testing.T, answers map[string][]net.IP) {
	t.Helper()
	prev := webLookupIP
	webLookupIP = func(host string) ([]net.IP, error) {
		if ips, ok := answers[host]; ok {
			return ips, nil
		}
		return nil, errors.New("no such host")
	}
	resetVerdicts := func() {
		hostVerdicts.mu.Lock()
		hostVerdicts.entries = map[string]hostVerdict{}
		hostVerdicts.mu.Unlock()
	}
	resetVerdicts()
	t.Cleanup(func() {
		webLookupIP = prev
		resetVerdicts()
	})
}

func TestBlockedNetTables_AllParsed(t *testing.T) {
	if len(alwaysBlockedNets) != 5 || len(embeddedIPv4Prefixes) != 2 {
		t.Fatalf("CIDR tables lost entries: %d always-blocked, %d NAT64", len(alwaysBlockedNets), len(embeddedIPv4Prefixes))
	}
}

func TestCheckWebIP_AlwaysBlockedSet(t *testing.T) {
	blocked := []string{
		"fd00:ec2::254",      // AWS IPv6 IMDS
		"fd00:ec2::23",       // EKS Pod Identity
		"100.100.100.200",    // Alibaba Cloud metadata
		"168.63.129.16",      // Azure WireServer
		"64:ff9b::a9fe:a9fe", // NAT64 of 169.254.169.254
		"64:ff9b::6464:64c8", // NAT64 of 100.100.100.200
		"64:ff9b:1::a9fe:a9fe",
		"0.0.0.0",
		"0.1.2.3",
		"::",
	}
	for _, s := range blocked {
		if err := checkWebIP(net.ParseIP(s), false, "h"); err == nil {
			t.Errorf("checkWebIP(%s) allowed, want blocked even with private ranges allowed", s)
		}
	}
	allowed := []string{"64:ff9b::808:808", "100.64.0.1", "203.0.113.10", "127.0.0.1", "fd00:1::1"}
	for _, s := range allowed {
		if err := checkWebIP(net.ParseIP(s), false, "h"); err != nil {
			t.Errorf("checkWebIP(%s) = %v, want allowed", s, err)
		}
	}
	// A NAT64 address of a private IPv4 follows the private-range policy.
	if err := checkWebIP(net.ParseIP("64:ff9b::a00:1"), true, "h"); err == nil {
		t.Error("NAT64 of 10.0.0.1 must be blocked when private ranges are blocked")
	}
}

// Chromium resolves names itself, so the render guard must resolve too
// and refuse a name that points at the metadata service, with private
// ranges allowed (the default).
func TestRenderRequestAllowed_HostnameResolvingToMetadata(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "")
	stubWebLookup(t, map[string][]net.IP{
		"rebind.example.test": {net.ParseIP("169.254.169.254")},
		"imds6.example.test":  {net.ParseIP("fd00:ec2::254")},
		"public.example.test": {net.ParseIP("203.0.113.10")},
	})
	if renderRequestAllowed("http://rebind.example.test/latest/meta-data/") {
		t.Error("hostname resolving to 169.254.169.254 must be blocked")
	}
	if renderRequestAllowed("http://imds6.example.test/") {
		t.Error("hostname resolving to the AWS IPv6 IMDS must be blocked")
	}
	if !renderRequestAllowed("https://public.example.test/app.js") {
		t.Error("hostname resolving to a public address must be allowed")
	}
	if renderRequestAllowed("https://unresolvable.example.test/") {
		t.Error("an unresolvable hostname with no proxy must be refused")
	}
}

func TestRenderRequestAllowed_UnresolvableThroughProxy(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "")
	t.Setenv("HTTPS_PROXY", "http://proxy.example.test:3128")
	t.Setenv("NO_PROXY", "")
	stubWebLookup(t, nil)
	if !renderRequestAllowed("https://only-the-proxy-resolves.example.test/") {
		t.Error("a name only the proxy can resolve must be allowed when a proxy carries the request")
	}
}

func TestValidateRedirect_CustomCredentialHeaders(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "")
	mk := func(u string) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, u, nil)
		return r
	}
	orig := mk("https://api.example.com/v1")

	cross := mk("https://cdn.example.net/x")
	cross.Header.Set("X-Subscription-Token", "secret")
	cross.Header.Set("Private-Token", "secret")
	cross.Header.Set("X-Goog-Api-Key", "secret")
	cross.Header.Set("User-Agent", "ua")
	cross.Header.Set("Accept", "text/html")
	if err := validateRedirect(cross, []*http.Request{orig}); err != nil {
		t.Fatalf("cross-host redirect: %v", err)
	}
	for _, h := range []string{"X-Subscription-Token", "Private-Token", "X-Goog-Api-Key"} {
		if cross.Header.Get(h) != "" {
			t.Errorf("%s must be dropped on a cross-host redirect", h)
		}
	}
	if cross.Header.Get("User-Agent") != "ua" || cross.Header.Get("Accept") != "text/html" {
		t.Error("content-negotiation headers must survive a cross-host redirect")
	}

	same := mk("https://api.example.com/v2")
	same.Header.Set("X-Subscription-Token", "secret")
	if err := validateRedirect(same, []*http.Request{orig}); err != nil {
		t.Fatalf("same-host redirect: %v", err)
	}
	if same.Header.Get("X-Subscription-Token") == "" {
		t.Error("a same-host redirect keeps the credential")
	}

	down := mk("http://api.example.com/v1")
	down.Header.Set("Authorization", "Bearer secret")
	if err := validateRedirect(down, []*http.Request{orig}); err == nil {
		t.Error("an https to http redirect carrying credentials must be refused")
	}
	plain := mk("http://api.example.com/v1")
	plain.Header.Set("User-Agent", "ua")
	if err := validateRedirect(plain, []*http.Request{orig}); err != nil {
		t.Errorf("an https to http redirect without credentials stays allowed: %v", err)
	}
}

func TestBrowserNavigationAllowed(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "")
	stubWebLookup(t, map[string][]net.IP{
		"localhost":           {net.ParseIP("127.0.0.1")},
		"rebind.example.test": {net.ParseIP("169.254.169.254")},
		"public.example.test": {net.ParseIP("203.0.113.10")},
	})
	for _, u := range []string{
		"file:///etc/passwd",
		"FILE:///etc/hosts",
		"javascript:alert(1)",
		"data:text/html,<h1>x</h1>",
		"chrome://settings",
		"view-source:https://public.example.test/",
		"http://169.254.169.254/latest/meta-data/",
		"http://[fd00:ec2::254]/latest/meta-data/",
		"http://100.100.100.200/latest/meta-data/",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://rebind.example.test/",
	} {
		if err := browserNavigationAllowed(u); err == nil {
			t.Errorf("browserNavigationAllowed(%q) = nil, want refused", u)
		}
	}
	for _, u := range []string{"about:blank", "http://localhost:3000", "https://public.example.test/page"} {
		if err := browserNavigationAllowed(u); err != nil {
			t.Errorf("browserNavigationAllowed(%q) = %v, want allowed", u, err)
		}
	}
}

func TestBrowserOpen_RefusesFileScheme(t *testing.T) {
	fb := withFakeBrowser(t)
	_, err := (&BuiltinBrowserPlugin{}).Execute(context.Background(), []string{`{"cmd":"open","args":{"url":"file:///etc/passwd"}}`})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("open file: must be refused, got %v", err)
	}
	if fb.lastOp == "navigate" {
		t.Fatalf("the browser must not navigate, got %q", fb.lastTarget)
	}
}

func TestBrowserArtifactPath_ConfinedToScratch(t *testing.T) {
	scratch := t.TempDir()
	t.Setenv("CHATCLI_AGENT_TMPDIR", scratch)
	resolved, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []string{"/etc/cron.d/evil.png", "../../outside.png", filepath.Join(os.Getenv("HOME"), ".bashrc"), "shot.png"} {
		p, err := browserArtifactPath(req, "screenshot", ".png")
		if err != nil {
			t.Fatalf("browserArtifactPath(%q): %v", req, err)
		}
		dir, _ := filepath.EvalSymlinks(filepath.Dir(p))
		if dir != resolved {
			t.Errorf("browserArtifactPath(%q) = %q, outside the scratch dir %q", req, p, scratch)
		}
	}
	if p, err := browserArtifactPath("", "page", ".pdf"); err != nil || !strings.HasSuffix(p, ".pdf") {
		t.Errorf("default name = %q, %v", p, err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "target"), filepath.Join(scratch, "link.png")); err == nil {
		if _, err := browserArtifactPath("link.png", "screenshot", ".png"); err == nil {
			t.Error("a planted symlink in the scratch dir must be refused")
		}
	}
}

func TestBrowserScreenshot_WritesInsideScratch(t *testing.T) {
	scratch := t.TempDir()
	t.Setenv("CHATCLI_AGENT_TMPDIR", scratch)
	resolved, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		t.Fatal(err)
	}
	fb := withFakeBrowser(t)
	for _, payload := range []string{
		`{"cmd":"screenshot","args":{"file":"/tmp/elsewhere/evil.png"}}`,
		`{"cmd":"screenshot","args":{"full":true,"file":"../../evil.png"}}`,
		`{"cmd":"pdf","args":{"file":"/etc/evil.png"}}`,
	} {
		if _, err := (&BuiltinBrowserPlugin{}).Execute(context.Background(), []string{payload}); err != nil {
			t.Fatalf("%s: %v", payload, err)
		}
		dir, _ := filepath.EvalSymlinks(filepath.Dir(fb.lastTarget))
		if dir != resolved || filepath.Base(fb.lastTarget) != "evil.png" {
			t.Fatalf("%s wrote %q, want evil.png inside %q", payload, fb.lastTarget, scratch)
		}
	}
}

// The scheduler builds its webhook and http_status clients from this.
func TestNewGuardedHTTPClient_RefusesMetadataRedirect(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()
	c := NewGuardedHTTPClient(5*time.Second, false)
	if c.CheckRedirect == nil {
		t.Fatal("guarded client must validate redirects")
	}
	resp, err := c.Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("redirect to the metadata service must be refused")
	}
}
