/*
 * ChatCLI - SSE transport OAuth header tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package mcp

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/diillson/chatcli/auth"
	"go.uber.org/zap"
)

// stubTokenProvider is a minimal auth.TokenProvider with a scripted answer.
type stubTokenProvider struct {
	tok string
	err error
}

func (s *stubTokenProvider) Token(context.Context) (string, error) { return s.tok, s.err }
func (s *stubTokenProvider) Mode() auth.AuthMode                   { return auth.AuthModeOAuth }
func (s *stubTokenProvider) Provider() auth.ProviderID             { return auth.ProviderMCP }
func (s *stubTokenProvider) ProfileID() string                     { return "" }
func (s *stubTokenProvider) Source() string                        { return "test" }
func (s *stubTokenProvider) Email() string                         { return "" }
func (s *stubTokenProvider) Invalidate()                           {}
func (s *stubTokenProvider) Close()                                {}

func newHeaderTestTransport(p auth.TokenProvider) *sseTransport {
	return &sseTransport{
		baseURL:       "https://mcp.example",
		serverName:    "aws",
		logger:        zap.NewNop(),
		tokenProvider: p,
	}
}

func TestSSEApplyHeaders_BearerFromProvider(t *testing.T) {
	tr := newHeaderTestTransport(&stubTokenProvider{tok: "live-token"})
	req, _ := http.NewRequest(http.MethodGet, "https://mcp.example/sse", nil)

	if err := tr.applyHeaders(req); err != nil {
		t.Fatalf("applyHeaders: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer live-token" {
		t.Errorf("Authorization = %q, want Bearer live-token", got)
	}
}

func TestSSEApplyHeaders_TerminalTokenErrorSurfacesOAuthRequired(t *testing.T) {
	terminal := &auth.TokenExchangeError{
		StatusCode: http.StatusUnauthorized,
		Status:     "401 Unauthorized",
		Code:       "TOKEN_EXPIRED",
	}
	tr := newHeaderTestTransport(&stubTokenProvider{err: terminal})
	req, _ := http.NewRequest(http.MethodPost, "https://mcp.example/messages", nil)

	err := tr.applyHeaders(req)
	oe, ok := IsOAuthRequired(err)
	if !ok {
		t.Fatalf("applyHeaders error = %v, want *OAuthRequiredError", err)
	}
	if oe.Server != "aws" {
		t.Errorf("OAuthRequiredError.Server = %q, want aws", oe.Server)
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("no Authorization header must be sent with a dead refresh token")
	}
}

func TestSSEApplyHeaders_TransientTokenErrorFallsBack(t *testing.T) {
	tr := newHeaderTestTransport(&stubTokenProvider{err: errors.New("connection reset")})
	req, _ := http.NewRequest(http.MethodGet, "https://mcp.example/sse", nil)

	// A transient refresh failure must not kill the stream: no error, no
	// bearer — the static auth fallback (nil here) applies and the server
	// answers 401 if it cares.
	if err := tr.applyHeaders(req); err != nil {
		t.Fatalf("applyHeaders: %v", err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("transient failure must not fabricate an Authorization header")
	}
}
