/*
 * ChatCLI - MCP OAuth tests (helpers, providers, discovery edges)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/auth"
	"go.uber.org/zap"
)

func TestOAuthRequiredErrorAndIs(t *testing.T) {
	oe := &OAuthRequiredError{Server: "aws", Endpoint: "https://x/mcp"}
	if oe.Error() == "" {
		t.Fatalf("Error() empty")
	}
	// errors.As through a wrap.
	wrapped := fmt.Errorf("initialize: %w", oe)
	got, ok := IsOAuthRequired(wrapped)
	if !ok || got.Server != "aws" {
		t.Fatalf("IsOAuthRequired failed to unwrap: %v", wrapped)
	}
	if _, ok := IsOAuthRequired(fmt.Errorf("plain")); ok {
		t.Fatalf("plain error should not be OAuthRequired")
	}
}

func TestOAuthHelpers(t *testing.T) {
	if mcpOAuthProfileID("aws") != "mcp:aws" {
		t.Errorf("profile id wrong")
	}

	if _, err := originOf("://bad"); err == nil {
		t.Errorf("expected error for bad URL")
	}
	origin, err := originOf("https://host.example.com/mcp/path")
	if err != nil || origin != "https://host.example.com" {
		t.Errorf("origin = %q err=%v", origin, err)
	}

	// chooseScope precedence: challenge > AS metadata > empty.
	if s := chooseScope(oauthChallenge{Scope: "a b"}, &authServerMetadata{ScopesSupported: []string{"x"}}); s != "a b" {
		t.Errorf("challenge scope should win, got %q", s)
	}
	if s := chooseScope(oauthChallenge{}, &authServerMetadata{ScopesSupported: []string{"x", "y"}}); s != "x y" {
		t.Errorf("AS scopes should join, got %q", s)
	}
	if s := chooseScope(oauthChallenge{}, &authServerMetadata{}); s != "" {
		t.Errorf("empty scope expected, got %q", s)
	}
}

func TestMCPOAuthLoopbackPort(t *testing.T) {
	t.Setenv("CHATCLI_MCP_OAUTH_PORT", "")
	if mcpOAuthLoopbackPort() != "8765" {
		t.Errorf("default port wrong")
	}
	t.Setenv("CHATCLI_MCP_OAUTH_PORT", "9999")
	if mcpOAuthLoopbackPort() != "9999" {
		t.Errorf("env override not honored")
	}
}

func TestOAuthMetaPathSanitizes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p, err := oauthMetaPath("weird/../name space")
	if err != nil {
		t.Fatalf("oauthMetaPath: %v", err)
	}
	// No path separators or spaces may survive into the filename, so the
	// sanitized name cannot escape the mcp_oauth directory.
	base := filepath.Base(p)
	if strings.ContainsAny(base, "/ ") || strings.Contains(p, "name space") {
		t.Errorf("unsanitized filename: base=%q full=%q", base, p)
	}
	if !strings.HasSuffix(base, ".json") {
		t.Errorf("missing .json suffix: %q", base)
	}
}

func TestHasStoredAndTokenProviderAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if hasStoredMCPOAuth("nope", zap.NewNop()) {
		t.Errorf("expected no stored oauth for fresh HOME")
	}
	provider, err := mcpTokenProvider("nope", "https://x/mcp", zap.NewNop())
	if err != nil {
		t.Fatalf("mcpTokenProvider err: %v", err)
	}
	if provider != nil {
		t.Errorf("expected nil provider when no creds stored")
	}
	// LogoutServer is a no-op when nothing is stored.
	if err := LogoutServer("nope", zap.NewNop()); err != nil {
		t.Errorf("LogoutServer should be a no-op, got %v", err)
	}
	// loadOAuthMeta returns an error for a missing file.
	if _, err := loadOAuthMeta("nope"); err == nil {
		t.Errorf("expected error loading missing meta")
	}
}

// TestStoredOAuthProvider covers the success branches: with a persisted
// credential + metadata, hasStoredMCPOAuth reports true and mcpTokenProvider
// returns a live provider.
func TestStoredOAuthProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CHATCLI_AUTH_DIR", t.TempDir())

	if err := saveOAuthMeta(&oauthServerMeta{
		Server:                "aws",
		Resource:              "https://x/mcp",
		Issuer:                "https://as",
		AuthorizationEndpoint: "https://as/authorize",
		TokenEndpoint:         "https://as/token",
		ClientID:              "c1",
		RedirectURI:           "http://127.0.0.1:8765/callback",
	}); err != nil {
		t.Fatalf("saveOAuthMeta: %v", err)
	}
	cred := &auth.AuthProfileCredential{
		CredType: auth.CredentialOAuth,
		Provider: auth.ProviderMCP,
		Access:   "at",
		Refresh:  "rt",
		Expires:  auth.TokenExpiryMilli(3600),
		ClientID: "c1",
	}
	if err := auth.UpsertProfile(mcpOAuthProfileID("aws"), cred, zap.NewNop()); err != nil {
		t.Fatalf("UpsertProfile: %v", err)
	}

	if !hasStoredMCPOAuth("aws", zap.NewNop()) {
		t.Fatalf("expected hasStoredMCPOAuth true")
	}
	provider, err := mcpTokenProvider("aws", "https://x/mcp", zap.NewNop())
	if err != nil {
		t.Fatalf("mcpTokenProvider: %v", err)
	}
	if provider == nil {
		t.Fatalf("expected non-nil provider")
	}
	defer provider.Close()
	tok, err := provider.Token(context.Background())
	if err != nil || tok != "at" {
		t.Fatalf("Token = %q err=%v", tok, err)
	}

	// LogoutServer removes both the credential and the metadata file.
	if err := LogoutServer("aws", zap.NewNop()); err != nil {
		t.Fatalf("LogoutServer: %v", err)
	}
	if hasStoredMCPOAuth("aws", zap.NewNop()) {
		t.Fatalf("expected credential gone after logout")
	}
}

func TestBindLoopback(t *testing.T) {
	// Use an ephemeral port to avoid clashing with a real 8765 on the runner.
	t.Setenv("CHATCLI_MCP_OAUTH_PORT", "0")
	ln, redirect, err := bindLoopback()
	if err != nil {
		t.Fatalf("bindLoopback: %v", err)
	}
	defer ln.Close()
	if redirect == "" || redirect[:17] != "http://127.0.0.1:" {
		t.Errorf("unexpected redirect URI: %q", redirect)
	}
}

func TestWriteCallbackHTML(t *testing.T) {
	rec := httptest.NewRecorder()
	writeCallbackHTML(rec, http.StatusOK, "<html>ok</html>")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
	if rec.Body.String() != "<html>ok</html>" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestRegisterClientErrors(t *testing.T) {
	disc := newOAuthDiscoverer(zap.NewNop())
	// No registration endpoint.
	if _, err := disc.registerClient(context.Background(), "", "http://127.0.0.1:0/callback", ""); err == nil {
		t.Errorf("expected error for empty registration endpoint")
	}
	// Non-2xx response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client_metadata"})
	}))
	defer srv.Close()
	if _, err := disc.registerClient(context.Background(), srv.URL, "http://127.0.0.1:0/callback", ""); err == nil {
		t.Errorf("expected error for non-2xx registration")
	}
}

func TestFetchAuthServerMetadataIncomplete(t *testing.T) {
	// Serves metadata missing the token endpoint → must be rejected.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(authServerMetadata{AuthorizationEndpoint: "https://x/authorize"})
	}))
	defer srv.Close()
	disc := newOAuthDiscoverer(zap.NewNop())
	if _, err := disc.fetchAuthServerMetadata(context.Background(), srv.URL); err == nil {
		t.Errorf("expected error for incomplete AS metadata")
	}
}

// TestDiscoverFallbackToOriginIssuer exercises the path where the server does
// not publish RFC 9728 protected-resource metadata and we treat the server
// origin as the issuer.
func TestDiscoverFallbackToOriginIssuer(t *testing.T) {
	mux := http.NewServeMux()
	var base string
	// No /.well-known/oauth-protected-resource handler → 404.
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(authServerMetadata{
			Issuer:                base,
			AuthorizationEndpoint: base + "/authorize",
			TokenEndpoint:         base + "/token",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	disc := newOAuthDiscoverer(zap.NewNop())
	asMeta, prm, err := disc.discover(context.Background(), base+"/mcp", oauthChallenge{})
	if err != nil {
		t.Fatalf("discover fallback: %v", err)
	}
	if asMeta.TokenEndpoint != base+"/token" {
		t.Errorf("token endpoint = %q", asMeta.TokenEndpoint)
	}
	if prm.Resource != base {
		t.Errorf("resource fallback = %q, want origin %q", prm.Resource, base)
	}
}
