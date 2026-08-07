/*
 * ChatCLI - MCP OAuth tests
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
	"testing"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

func TestParseWWWAuthenticate(t *testing.T) {
	cases := []struct {
		name     string
		header   string
		present  bool
		resource string
		scope    string
	}{
		{"empty", "", false, "", ""},
		{"non-bearer", `Basic realm="x"`, false, "", ""},
		{
			"full",
			`Bearer realm="mcp", resource_metadata="https://as.example.com/.well-known/oauth-protected-resource", scope="a b"`,
			true,
			"https://as.example.com/.well-known/oauth-protected-resource",
			"a b",
		},
		{"bare bearer", "Bearer", true, "", ""},
		{
			"quoted comma in scope",
			`Bearer scope="read, write", resource_metadata="https://x/y"`,
			true,
			"https://x/y",
			"read, write",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ch := parseWWWAuthenticate(c.header)
			if ch.Present != c.present {
				t.Fatalf("Present = %v, want %v", ch.Present, c.present)
			}
			if ch.ResourceMetadata != c.resource {
				t.Errorf("ResourceMetadata = %q, want %q", ch.ResourceMetadata, c.resource)
			}
			if ch.Scope != c.scope {
				t.Errorf("Scope = %q, want %q", ch.Scope, c.scope)
			}
		})
	}
}

func TestWellKnownCandidates(t *testing.T) {
	// Issuer without a path → oauth-as + oidc on the origin.
	got := wellKnownCandidates("https://as.example.com")
	if len(got) != 2 || got[0] != "https://as.example.com/.well-known/oauth-authorization-server" {
		t.Fatalf("no-path candidates wrong: %v", got)
	}
	// Issuer with a path → RFC 8414 host-insertion must come first.
	got = wellKnownCandidates("https://as.example.com/tenant1")
	if got[0] != "https://as.example.com/.well-known/oauth-authorization-server/tenant1" {
		t.Fatalf("host-insertion candidate missing/first wrong: %v", got)
	}
}

func TestOAuthMetaRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	meta := &oauthServerMeta{
		Server:                "aws",
		Issuer:                "https://as.example.com",
		AuthorizationEndpoint: "https://as.example.com/authorize",
		TokenEndpoint:         "https://as.example.com/token",
		ClientID:              "client-123",
		RedirectURI:           "http://127.0.0.1:8765/callback",
	}
	if err := saveOAuthMeta(meta); err != nil {
		t.Fatalf("saveOAuthMeta: %v", err)
	}
	got, err := loadOAuthMeta("aws")
	if err != nil {
		t.Fatalf("loadOAuthMeta: %v", err)
	}
	if got.ClientID != "client-123" || got.TokenEndpoint != meta.TokenEndpoint {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

// TestDiscoverResolvesAuthServer exercises the RFC 9728 → RFC 8414 chain end to
// end against an in-process server that plays both the protected resource and
// its authorization server.
func TestDiscoverResolvesAuthServer(t *testing.T) {
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(protectedResourceMetadata{
			Resource:             base + "/mcp",
			AuthorizationServers: []string{base},
			ScopesSupported:      []string{"mcp"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(authServerMetadata{
			Issuer:                base,
			AuthorizationEndpoint: base + "/authorize",
			TokenEndpoint:         base + "/token",
			RegistrationEndpoint:  base + "/register",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	disc := newOAuthDiscoverer(zap.NewNop())
	asMeta, prm, err := disc.discover(context.Background(), base+"/mcp", oauthChallenge{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if asMeta.TokenEndpoint != base+"/token" || asMeta.AuthorizationEndpoint != base+"/authorize" {
		t.Fatalf("unexpected AS metadata: %+v", asMeta)
	}
	if prm.Resource != base+"/mcp" {
		t.Errorf("resource = %q, want %q", prm.Resource, base+"/mcp")
	}
}

func TestRegisterClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["token_endpoint_auth_method"] != "none" {
			t.Errorf("expected public client, got %v", body["token_endpoint_auth_method"])
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "dyn-client-9"})
	}))
	defer srv.Close()

	disc := newOAuthDiscoverer(zap.NewNop())
	id, err := disc.registerClient(context.Background(), srv.URL, "http://127.0.0.1:8765/callback", "mcp")
	if err != nil {
		t.Fatalf("registerClient: %v", err)
	}
	if id != "dyn-client-9" {
		t.Fatalf("client_id = %q, want dyn-client-9", id)
	}
}

// TestMCPRefreshFunc verifies the refresh closure posts a refresh_token grant
// with the resource indicator and updates the credential in place.
func TestMCPRefreshFunc(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// MCP token exchange is form-encoded (RFC 6749 §4.1.3).
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("content-type = %q, want form-urlencoded", ct)
		}
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %v, want refresh_token", r.PostForm.Get("grant_type"))
		}
		if r.PostForm.Get("resource") != "https://srv/mcp" {
			t.Errorf("resource = %v, want https://srv/mcp", r.PostForm.Get("resource"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	defer srv.Close()

	fn := newMCPRefreshFunc(srv.URL, "client-1", "https://srv/mcp", "mcp")
	cred := &auth.AuthProfileCredential{
		CredType: auth.CredentialOAuth,
		Provider: auth.ProviderMCP,
		Access:   "old-access",
		Refresh:  "old-refresh",
	}
	out, err := fn(context.Background(), cred, zap.NewNop())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if out.Access != "new-access" || out.Refresh != "new-refresh" {
		t.Fatalf("credential not updated: %+v", out)
	}
	if out.Expires <= 0 {
		t.Errorf("expiry not set")
	}
}

// TestLoginClientID_AlwaysRegistersFreshWhenDCRAvailable: a stored dynamic
// client registration can be expired or purged by the AS at any time
// (observed on AWS signin: authorize with the stale client_id fails on the
// AS's own page with an invalid-redirect_uri error AFTER the user logs in,
// and every retry reuses the same dead client — no recovery path). When the
// AS offers a registration endpoint, an interactive login must therefore
// ALWAYS register a fresh client, even when the stored one matches the
// redirect_uri.
func TestLoginClientID_AlwaysRegistersFreshWhenDCRAvailable(t *testing.T) {
	registered := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registered++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"client_id":"fresh-123"}`))
	}))
	defer srv.Close()

	disc := newOAuthDiscoverer(zap.NewNop())
	asMeta := &authServerMetadata{RegistrationEndpoint: srv.URL}
	stored := &oauthServerMeta{ClientID: "stale-999", RedirectURI: "http://127.0.0.1:8765/callback"}

	id, err := loginClientID(context.Background(), disc, asMeta, stored, "http://127.0.0.1:8765/callback", "scope")
	if err != nil {
		t.Fatalf("loginClientID: %v", err)
	}
	if id != "fresh-123" {
		t.Fatalf("client id = %q, want fresh-123 (stale stored registration must never be reused when DCR is available)", id)
	}
	if registered != 1 {
		t.Fatalf("registration endpoint hit %d times, want 1", registered)
	}
}

// TestLoginClientID_ReusesStoredWithoutDCR: with no registration endpoint the
// stored client is a manual registration — reuse it when the redirect_uri
// matches, and fail with the actionable no-DCR error when it does not.
func TestLoginClientID_ReusesStoredWithoutDCR(t *testing.T) {
	disc := newOAuthDiscoverer(zap.NewNop())
	asMeta := &authServerMetadata{} // no RegistrationEndpoint

	stored := &oauthServerMeta{ClientID: "manual-42", RedirectURI: "http://127.0.0.1:8765/callback"}
	id, err := loginClientID(context.Background(), disc, asMeta, stored, "http://127.0.0.1:8765/callback", "")
	if err != nil || id != "manual-42" {
		t.Fatalf("id=%q err=%v, want manual-42 nil (manual client must be reused)", id, err)
	}

	// redirect mismatch (e.g. fixed port busy, ephemeral fallback): the manual
	// client cannot serve this redirect and there is no DCR — actionable error.
	if _, err := loginClientID(context.Background(), disc, asMeta, stored, "http://127.0.0.1:54321/callback", ""); err == nil {
		t.Fatal("redirect mismatch without DCR must error, not silently reuse a client bound to another redirect_uri")
	}

	// nothing stored and no DCR: same actionable error.
	if _, err := loginClientID(context.Background(), disc, asMeta, nil, "http://127.0.0.1:8765/callback", ""); err == nil {
		t.Fatal("no stored client and no DCR must error")
	}
}

// TestCallbackTimeoutErrorCarriesRecoveryHint: when nobody completes the
// browser flow, the most common real-world cause is the provider rejecting
// the authorization page under the browser's existing session (observed on
// AWS signin: a live IAM session gets routed to a path that answers 400,
// while an anonymous browser authorizes fine). The timeout error must carry
// the recovery hint — the printed one has long scrolled away, and this error
// is what the tool relays back to the user.
func TestCallbackTimeoutErrorCarriesRecoveryHint(t *testing.T) {
	err := callbackTimeoutError(5 * time.Minute)
	if err == nil {
		t.Fatal("timeout must produce an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, i18n.T("mcp.oauth.callback_timeout", 5*time.Minute)) {
		t.Fatalf("timeout error lost its base message: %s", msg)
	}
	if !strings.Contains(msg, i18n.T("mcp.oauth.stale_session_hint")) {
		t.Fatalf("timeout error must carry the stale-session recovery hint: %s", msg)
	}
}

// TestBuildAuthorizeURL_CarriesEveryRequiredParam: the authorization request
// must keep every parameter the server requires — AWS rejects the request
// outright without the RFC 8707 resource indicator, and answers 400 when the
// redirect does not match the registered one byte for byte.
func TestBuildAuthorizeURL_CarriesEveryRequiredParam(t *testing.T) {
	p := loopbackAuthParams{
		authorizationEndpoint: "https://as.example/v1/authorize",
		clientID:              "client-1",
		redirectURI:           "http://127.0.0.1:8765/callback",
		resource:              "https://mcp.example/mcp",
		scope:                 "read",
	}
	u, err := url.Parse(buildAuthorizeURL(p, "chal", "st8"))
	if err != nil {
		t.Fatalf("authorize URL is not parseable: %v", err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"response_type": "code", "client_id": "client-1",
		"redirect_uri": "http://127.0.0.1:8765/callback", "code_challenge": "chal",
		"code_challenge_method": "S256", "state": "st8", "scope": "read",
		"resource": "https://mcp.example/mcp",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}

}
