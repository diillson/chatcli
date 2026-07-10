/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// This file exposes a minimal, stable surface over otherwise-internal OAuth
// primitives so subsystems outside the provider logins (notably MCP OAuth,
// whose authorization/token endpoints are DISCOVERED at runtime rather than
// hard-coded per provider) can reuse the exact same, already-hardened
// building blocks instead of re-implementing them. Names are deliberately
// distinct from the internal helpers they wrap (not just case variants).

// browserLauncher is the function OpenInBrowser delegates to. It is a package
// variable (defaulting to the real cross-platform opener) purely so tests can
// substitute a stub and avoid actually launching a browser.
var browserLauncher = openBrowser

// OpenInBrowser opens the user's default browser at rawURL using the same
// cross-platform launch logic the provider login flows use. Returns an error
// when no browser could be launched so the caller can fall back to printing
// the URL.
func OpenInBrowser(rawURL string) error {
	return browserLauncher(rawURL)
}

// TokenExchangeRequest holds the form parameters for an OAuth token exchange.
// It is a concrete type (rather than a free-form map) so the exported surface
// stays type-safe; empty fields are omitted from the request body.
type TokenExchangeRequest struct {
	GrantType    string
	ClientID     string
	Code         string
	CodeVerifier string
	RedirectURI  string
	RefreshToken string
	Resource     string
	Scope        string
}

// pairs returns the non-empty (key, value) parameters in a stable order.
func (r TokenExchangeRequest) pairs() [][2]string {
	all := [][2]string{
		{"grant_type", r.GrantType},
		{"client_id", r.ClientID},
		{"code", r.Code},
		{"code_verifier", r.CodeVerifier},
		{"redirect_uri", r.RedirectURI},
		{"refresh_token", r.RefreshToken},
		{"resource", r.Resource},
		{"scope", r.Scope},
	}
	out := make([][2]string, 0, len(all))
	for _, kv := range all {
		if kv[1] != "" {
			out = append(out, kv)
		}
	}
	return out
}

func (r TokenExchangeRequest) toPayload() map[string]any {
	m := make(map[string]any, 8)
	for _, kv := range r.pairs() {
		m[kv[0]] = kv[1]
	}
	return m
}

func (r TokenExchangeRequest) toForm() url.Values {
	form := url.Values{}
	for _, kv := range r.pairs() {
		form.Set(kv[0], kv[1])
	}
	return form
}

// ExchangeToken performs an OAuth token exchange with a JSON body against
// tokenURL and returns the parsed token response. Used by providers that
// accept a JSON token request (Anthropic/OpenAI-style).
func ExchangeToken(ctx context.Context, logger *zap.Logger, tokenURL string, req TokenExchangeRequest) (*OAuthTokenResponse, error) {
	return exchangeOAuthToken(ctx, logger, tokenURL, req.toPayload())
}

// ExchangeTokenForm performs an OAuth token exchange with an
// application/x-www-form-urlencoded body — the encoding RFC 6749 §4.1.3
// mandates for the token endpoint and what standards-compliant authorization
// servers (e.g. AWS, Cognito, Keycloak) require. This is the correct exchange
// for MCP OAuth, whose authorization servers are generic OAuth 2.1 endpoints
// rather than the JSON-accepting LLM-provider endpoints.
func ExchangeTokenForm(ctx context.Context, logger *zap.Logger, tokenURL string, req TokenExchangeRequest) (*OAuthTokenResponse, error) {
	hc := utils.NewHTTPClient(logger, 30*time.Second)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(req.toForm().Encode()))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/json")
	return doTokenExchange(hc, httpReq)
}

// TokenExpiryMilli converts an OAuth expires_in (seconds) into an absolute
// epoch-millisecond expiry, applying the shared 5-minute safety margin so all
// credentials refresh consistently ahead of the real upstream expiry.
func TokenExpiryMilli(expiresIn int64) int64 {
	return calcExpiresAtMilli(expiresIn)
}
