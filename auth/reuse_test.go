/*
 * ChatCLI - tests for the reusable OAuth surface
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestOpenInBrowser(t *testing.T) {
	old := browserLauncher
	t.Cleanup(func() { browserLauncher = old })
	var got string
	browserLauncher = func(u string) error { got = u; return nil }
	if err := OpenInBrowser("http://127.0.0.1/x"); err != nil {
		t.Fatalf("OpenInBrowser: %v", err)
	}
	if got != "http://127.0.0.1/x" {
		t.Fatalf("launcher got %q", got)
	}
}

func TestTokenExpiryMilli(t *testing.T) {
	if TokenExpiryMilli(3600) <= 0 {
		t.Fatalf("expected positive expiry")
	}
	// Zero/negative expires_in falls back to a default future expiry.
	if TokenExpiryMilli(0) <= 0 {
		t.Fatalf("expected fallback expiry for 0")
	}
}

func TestExchangeTokenOmitsEmptyFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["scope"]; ok {
			t.Errorf("empty scope must be omitted, got %v", body["scope"])
		}
		if body["grant_type"] != "authorization_code" {
			t.Errorf("grant_type = %v", body["grant_type"])
		}
		if body["code"] != "abc" {
			t.Errorf("code = %v", body["code"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "expires_in": 3600, "token_type": "Bearer",
		})
	}))
	defer srv.Close()

	tr, err := ExchangeToken(context.Background(), zap.NewNop(), srv.URL, TokenExchangeRequest{
		GrantType: "authorization_code",
		ClientID:  "c1",
		Code:      "abc",
	})
	if err != nil {
		t.Fatalf("ExchangeToken: %v", err)
	}
	if tr.AccessToken != "at" {
		t.Fatalf("access token = %q", tr.AccessToken)
	}
}

func TestExchangeTokenForm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("content-type = %q, want form-urlencoded", ct)
		}
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q", r.PostForm.Get("grant_type"))
		}
		if r.PostForm.Get("code") != "abc" || r.PostForm.Get("resource") != "https://x/mcp" {
			t.Errorf("missing form fields: %v", r.PostForm)
		}
		if _, ok := r.PostForm["scope"]; ok {
			t.Errorf("empty scope must be omitted")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "expires_in": 3600})
	}))
	defer srv.Close()

	tr, err := ExchangeTokenForm(context.Background(), zap.NewNop(), srv.URL, TokenExchangeRequest{
		GrantType:   "authorization_code",
		ClientID:    "c1",
		Code:        "abc",
		RedirectURI: "http://127.0.0.1:8765/callback",
		Resource:    "https://x/mcp",
	})
	if err != nil {
		t.Fatalf("ExchangeTokenForm: %v", err)
	}
	if tr.AccessToken != "at" {
		t.Fatalf("access token = %q", tr.AccessToken)
	}
}

// TestNewOAuthTokenProviderWithRefresh proves the custom refresh function is
// the one used (not the built-in provider switch), and that a Token() call on
// an expired credential triggers it.
func TestNewOAuthTokenProviderWithRefresh(t *testing.T) {
	called := 0
	refresh := func(_ context.Context, cred *AuthProfileCredential, _ *zap.Logger) (*AuthProfileCredential, error) {
		called++
		cred.Access = "refreshed"
		cred.Expires = TokenExpiryMilli(3600)
		return cred, nil
	}
	cred := &AuthProfileCredential{
		CredType: CredentialOAuth,
		Provider: ProviderMCP,
		Access:   "stale",
		Refresh:  "r1",
		Expires:  1, // already expired → forces refresh on first Token()
	}
	p := NewOAuthTokenProviderWithRefresh(cred, "", "test", refresh, zap.NewNop())
	defer p.Close()

	tok, err := p.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "refreshed" || called == 0 {
		t.Fatalf("custom refresh not used: tok=%q called=%d", tok, called)
	}
	if p.Mode() != AuthModeOAuth {
		t.Fatalf("mode = %v", p.Mode())
	}
	if p.Provider() != ProviderMCP {
		t.Fatalf("provider = %v", p.Provider())
	}
}

// A nil refresh function must fall back to the built-in RefreshOAuth without
// panicking; a non-refreshable credential (no refresh token) just serves its
// current token.
func TestNewOAuthTokenProviderNilRefreshFallback(t *testing.T) {
	cred := &AuthProfileCredential{
		CredType: CredentialOAuth,
		Provider: ProviderMCP,
		Access:   "tok",
		// no Refresh / Expires → no background loop, no refresh attempted
	}
	p := NewOAuthTokenProviderWithRefresh(cred, "", "test", nil, zap.NewNop())
	defer p.Close()
	tok, err := p.Token(context.Background())
	if err != nil || tok != "tok" {
		t.Fatalf("Token = %q err=%v", tok, err)
	}
}
