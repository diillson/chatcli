/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/diillson/chatcli/auth"
	"go.uber.org/zap"
)

// oauthFixture plays an MCP server, its protected-resource metadata and its
// authorization server on one origin, recording every resource indicator the
// client sends.
type oauthFixture struct {
	srv *httptest.Server
	// prmResource is the "resource" the protected-resource metadata claims.
	prmResource func(base string) string

	mu         sync.Mutex
	authorize  []string // resource of each /authorize request
	tokenGrant []url.Values
	prmHits    atomic.Int32
}

func newOAuthFixture(t *testing.T, prmResource func(base string) string) *oauthFixture {
	t.Helper()
	f := &oauthFixture{prmResource: prmResource}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, _ *http.Request) {
		f.prmHits.Add(1)
		_ = json.NewEncoder(w).Encode(protectedResourceMetadata{
			Resource:             f.prmResource(f.srv.URL),
			AuthorizationServers: []string{f.srv.URL},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		base := f.srv.URL
		_ = json.NewEncoder(w).Encode(authServerMetadata{
			Issuer:                base,
			AuthorizationEndpoint: base + "/authorize",
			TokenEndpoint:         base + "/token",
			RegistrationEndpoint:  base + "/register",
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "client-1"})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		f.authorize = append(f.authorize, q.Get("resource"))
		f.mu.Unlock()
		back := q.Get("redirect_uri") + "?code=code-1&state=" + url.QueryEscape(q.Get("state"))
		http.Redirect(w, r, back, http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.tokenGrant = append(f.tokenGrant, r.PostForm)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-1",
			"refresh_token": "refresh-1",
			"expires_in":    3600,
		})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// isolateOAuth points every piece of persisted OAuth state at a temporary
// directory and replaces the browser with a client that follows the
// authorization redirect back to the loopback callback.
func isolateOAuth(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CHATCLI_AUTH_DIR", t.TempDir())
	t.Setenv("CHATCLI_MCP_OAUTH_PORT", "0")
	prev := openAuthURL
	openAuthURL = func(u string) error {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		go func() {
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}
	t.Cleanup(func() { openAuthURL = prev })
}

// TestLoginRejectsMetadataNamingAnotherResource reproduces the reported
// attack: the MCP server's protected-resource metadata names a different
// resource of the same authorization server. The login must stop before the
// user is ever sent to consent, and nothing may be stored.
func TestLoginRejectsMetadataNamingAnotherResource(t *testing.T) {
	isolateOAuth(t)
	f := newOAuthFixture(t, func(string) string { return "https://victim.example/api" })
	serverURL := f.srv.URL + "/mcp"

	err := LoginServer(context.Background(), "evil", serverURL,
		oauthChallenge{ResourceMetadata: f.srv.URL + "/.well-known/oauth-protected-resource"}, zap.NewNop())
	if err == nil {
		t.Fatal("login succeeded with metadata naming another resource")
	}
	if !strings.Contains(err.Error(), "victim.example") {
		t.Errorf("error does not name the claimed resource: %v", err)
	}
	if len(f.authorize) != 0 || len(f.tokenGrant) != 0 {
		t.Errorf("authorization proceeded: authorize=%v token=%v", f.authorize, f.tokenGrant)
	}
	if hasStoredMCPOAuth("evil", serverURL, zap.NewNop()) {
		t.Error("a credential was stored")
	}
}

// TestLoginRejectsCrossOriginMetadataURL pins that the resource_metadata URL
// of a challenge is only followed on the server's own origin.
func TestLoginRejectsCrossOriginMetadataURL(t *testing.T) {
	isolateOAuth(t)
	elsewhere := newOAuthFixture(t, func(base string) string { return base + "/mcp" })
	f := newOAuthFixture(t, func(base string) string { return base + "/mcp" })

	err := LoginServer(context.Background(), "srv", f.srv.URL+"/mcp",
		oauthChallenge{ResourceMetadata: elsewhere.srv.URL + "/.well-known/oauth-protected-resource"}, zap.NewNop())
	if err == nil {
		t.Fatal("login followed a cross-origin resource_metadata URL")
	}
	if n := elsewhere.prmHits.Load(); n != 0 {
		t.Errorf("cross-origin metadata fetched %d times", n)
	}
}

// TestLoginBindsEveryGrantToTheServerURL covers the accepted case where the
// metadata describes the whole origin: the server URL, never the claim, is
// the resource indicator of the authorization request, the code exchange,
// the stored metadata and every refresh.
func TestLoginBindsEveryGrantToTheServerURL(t *testing.T) {
	isolateOAuth(t)
	f := newOAuthFixture(t, func(base string) string { return base })
	serverURL := f.srv.URL + "/mcp"

	if err := LoginServer(context.Background(), "srv", serverURL, oauthChallenge{}, zap.NewNop()); err != nil {
		t.Fatalf("login: %v", err)
	}
	if len(f.authorize) != 1 || f.authorize[0] != serverURL {
		t.Errorf("authorize resource = %v, want [%s]", f.authorize, serverURL)
	}
	if len(f.tokenGrant) != 1 || f.tokenGrant[0].Get("resource") != serverURL {
		t.Fatalf("token grants = %v", f.tokenGrant)
	}
	meta, err := loadOAuthMeta("srv")
	if err != nil || meta.Resource != serverURL {
		t.Fatalf("stored resource = %+v, %v", meta, err)
	}

	// Expire the access token so the provider refreshes it.
	cred := auth.GetProfile(mcpOAuthProfileID("srv"), zap.NewNop())
	cred.Expires = 1
	if err := auth.UpsertProfile(mcpOAuthProfileID("srv"), cred, zap.NewNop()); err != nil {
		t.Fatalf("expire credential: %v", err)
	}
	provider, err := mcpTokenProvider("srv", serverURL, zap.NewNop())
	if err != nil || provider == nil {
		t.Fatalf("provider = %v, %v", provider, err)
	}
	defer provider.Close()
	if _, err := provider.Token(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(f.tokenGrant) != 2 || f.tokenGrant[1].Get("grant_type") != "refresh_token" ||
		f.tokenGrant[1].Get("resource") != serverURL {
		t.Errorf("refresh grant = %v, want refresh_token for %s", f.tokenGrant, serverURL)
	}
}

// TestCredentialForAnotherResourceIsNeverSent covers state written by an
// affected release: metadata recording a foreign resource. Its credential
// must not be attached to requests, must not count as a login, and a new
// login must rediscover instead of trusting the recorded endpoints.
func TestCredentialForAnotherResourceIsNeverSent(t *testing.T) {
	isolateOAuth(t)
	f := newOAuthFixture(t, func(base string) string { return base + "/mcp" })
	serverURL := f.srv.URL + "/mcp"

	if err := saveOAuthMeta(&oauthServerMeta{
		Server:                "srv",
		Resource:              "https://victim.example/api",
		Issuer:                "https://attacker.example",
		AuthorizationEndpoint: "https://attacker.example/authorize",
		TokenEndpoint:         "https://attacker.example/token",
		ClientID:              "stale",
	}); err != nil {
		t.Fatal(err)
	}
	if err := auth.UpsertProfile(mcpOAuthProfileID("srv"), &auth.AuthProfileCredential{
		CredType: auth.CredentialOAuth,
		Provider: auth.ProviderMCP,
		Access:   "foreign-audience",
		Expires:  auth.TokenExpiryMilli(3600),
	}, zap.NewNop()); err != nil {
		t.Fatal(err)
	}

	if p, err := mcpTokenProvider("srv", serverURL, zap.NewNop()); p != nil || err != nil {
		t.Fatalf("provider for a foreign-audience credential = %v, %v", p, err)
	}
	if hasStoredMCPOAuth("srv", serverURL, zap.NewNop()) {
		t.Error("foreign-audience credential reported as a login")
	}

	if err := LoginServer(context.Background(), "srv", serverURL, oauthChallenge{}, zap.NewNop()); err != nil {
		t.Fatalf("login: %v", err)
	}
	if f.prmHits.Load() == 0 {
		t.Error("login reused the recorded endpoints instead of rediscovering")
	}
	if len(f.tokenGrant) != 1 || f.tokenGrant[0].Get("resource") != serverURL {
		t.Errorf("token grants = %v", f.tokenGrant)
	}
}

func TestDescribesResource(t *testing.T) {
	const resource = "https://mcp.example.com/tenant/mcp"
	cases := []struct {
		claimed string
		want    bool
	}{
		{"", true},
		{resource, true},
		{resource + "/", true},
		{"HTTPS://MCP.EXAMPLE.COM/tenant/mcp", true},
		{"https://mcp.example.com:443/tenant/mcp", true},
		{"https://mcp.example.com", true},
		{"https://mcp.example.com/tenant", true},
		{"https://mcp.example.com/ten", false},
		{"https://mcp.example.com/tenant/mcp/admin", false},
		{"https://mcp.example.com/other", false},
		{"http://mcp.example.com/tenant/mcp", false},
		{"https://mcp.example.com:8443/tenant/mcp", false},
		{"https://victim.example/api", false},
		{"https://www.google.com/url?q=https://victim.example/api", false},
		{"https://mcp.example.com/tenant/mcp?aud=other", false},
		{"https://mcp.example.com/tenant/mcp#x", false},
		{"not a url", false},
	}
	for _, c := range cases {
		if got := describesResource(c.claimed, resource); got != c.want {
			t.Errorf("describesResource(%q) = %v, want %v", c.claimed, got, c.want)
		}
	}
}

func TestCanonicalResource(t *testing.T) {
	got, err := canonicalResource(" HTTPS://MCP.Example.com/Path/MCP#frag ")
	if err != nil || got != "https://mcp.example.com/Path/MCP" {
		t.Errorf("canonicalResource = %q, %v", got, err)
	}
	if _, err := canonicalResource("/relative"); err == nil {
		t.Error("relative URL accepted")
	}
}

func TestPRMCandidatesPathInsertedFirst(t *testing.T) {
	got, err := prmCandidates("https://h.example/a/mcp", "https://h.example", oauthChallenge{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://h.example/.well-known/oauth-protected-resource/a/mcp",
		"https://h.example/.well-known/oauth-protected-resource",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("candidates = %v, want %v", got, want)
	}
}

func TestOAuthDocumentSizeIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"resource":"`))
		_, _ = w.Write([]byte(strings.Repeat("a", maxOAuthDocumentBytes)))
		_, _ = w.Write([]byte(`"}`))
	}))
	defer srv.Close()
	if _, err := fetchJSON[protectedResourceMetadata](context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("oversized document decoded")
	}
}
