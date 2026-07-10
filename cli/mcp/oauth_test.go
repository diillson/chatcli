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
	"testing"

	"github.com/diillson/chatcli/auth"
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
