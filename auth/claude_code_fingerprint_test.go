/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestClaudeCodeUserAgent_TracksTheVersion(t *testing.T) {
	if !strings.HasPrefix(ClaudeCodeUserAgent, "claude-cli/"+ClaudeCodeVersion+" ") || !strings.HasSuffix(ClaudeCodeUserAgent, "(external, cli)") {
		t.Fatalf("user agent = %q", ClaudeCodeUserAgent)
	}
	if strings.Count(ClaudeCodeVersion, ".") != 2 {
		t.Fatalf("version must be a release triple, got %q", ClaudeCodeVersion)
	}
}

func TestExchangeAnthropicToken_SendsClaudeCodeFingerprint(t *testing.T) {
	var ua, ct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		ct = r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600}`))
	}))
	defer srv.Close()
	tok, err := exchangeAnthropicToken(context.Background(), srv.URL, map[string]any{"grant_type": "authorization_code"})
	if err != nil || tok == nil || tok.AccessToken != "at" || tok.RefreshToken != "rt" {
		t.Fatalf("exchange: %+v %v", tok, err)
	}
	if ua != ClaudeCodeUserAgent || ct != "application/json" {
		t.Fatalf("headers: ua=%q ct=%q", ua, ct)
	}
}

func TestFetchAnthropicEmail_SendsClaudeCodeFingerprint(t *testing.T) {
	var ua, authz string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		authz = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"account":{"email":"person@example.com"}}`))
	}))
	defer srv.Close()
	if got := fetchAnthropicEmailFrom(context.Background(), srv.URL, "tok", zap.NewNop()); got != "person@example.com" {
		t.Fatalf("email = %q", got)
	}
	if ua != ClaudeCodeUserAgent || authz != "Bearer tok" {
		t.Fatalf("headers: ua=%q authz=%q", ua, authz)
	}
	if fetchAnthropicEmailFrom(context.Background(), srv.URL, "", zap.NewNop()) != "" {
		t.Fatal("empty token yields no email")
	}
}
