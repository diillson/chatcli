/*
 * ChatCLI - MCP OAuth loopback authorization-code flow
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Runs the browser-based authorization-code + PKCE exchange over a localhost
 * callback. Mirrors the hardened provider-login loopback (auth/login_flows.go)
 * — CSRF state validation, security headers, bounded timeouts — but drives a
 * runtime-discovered authorization endpoint and echoes the RFC 8707 resource
 * indicator so the issued token is bound to the target MCP server.
 */
package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// loopbackAuthParams carries the per-login parameters for the authorization
// request. The listener is passed separately (already bound) so the caller can
// register a client against the exact redirect_uri before we authorize.
type loopbackAuthParams struct {
	authorizationEndpoint string
	clientID              string
	redirectURI           string
	scope                 string
	resource              string
}

// buildAuthorizeURL assembles the authorization request. Split out from the
// browser flow so the exact wire parameters are unit-testable.
func buildAuthorizeURL(p loopbackAuthParams, challenge, state string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", p.clientID)
	q.Set("redirect_uri", p.redirectURI)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	if p.scope != "" {
		q.Set("scope", p.scope)
	}
	if p.resource != "" {
		// RFC 8707 resource indicator — binds the token to this MCP server.
		q.Set("resource", p.resource)
	}
	return p.authorizationEndpoint + "?" + q.Encode()
}

// runLoopbackAuth opens the browser at the authorization endpoint and waits for
// the callback, returning the authorization code and the PKCE verifier used.
// The listener is owned by the caller for binding, but this function serves and
// shuts it down.
func runLoopbackAuth(ctx context.Context, listener net.Listener, p loopbackAuthParams, logger *zap.Logger) (code string, verifier string, err error) {
	pkce, err := auth.GeneratePKCE()
	if err != nil {
		return "", "", err
	}
	state, err := auth.GenerateState()
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", i18n.T("mcp.oauth.state_failed"), err)
	}

	authURL := buildAuthorizeURL(p, pkce.Challenge, state)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	srv := &http.Server{
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       mcpOAuthCallbackTimeout,
	}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("Cache-Control", "no-store")

		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("state") != state {
			writeCallbackHTML(w, http.StatusForbidden, i18n.T("mcp.oauth.callback_csrf_html"))
			errCh <- fmt.Errorf("%s", i18n.T("mcp.oauth.callback_csrf"))
			return
		}
		if oauthErr := r.URL.Query().Get("error"); oauthErr != "" {
			desc := r.URL.Query().Get("error_description")
			writeCallbackHTML(w, http.StatusBadRequest, i18n.T("mcp.oauth.callback_error_html"))
			errCh <- fmt.Errorf("%s", i18n.T("mcp.oauth.callback_server_error", oauthErr, desc))
			return
		}
		c := r.URL.Query().Get("code")
		if c == "" {
			writeCallbackHTML(w, http.StatusBadRequest, i18n.T("mcp.oauth.callback_nocode_html"))
			errCh <- fmt.Errorf("%s", i18n.T("mcp.oauth.callback_nocode"))
			return
		}
		writeCallbackHTML(w, http.StatusOK, i18n.T("mcp.oauth.callback_success_html"))
		codeCh <- c
	})

	go func() {
		if serveErr := srv.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Println(i18n.T("mcp.oauth.opening_browser"))
	if openErr := auth.OpenInBrowser(authURL); openErr != nil {
		fmt.Println(i18n.T("mcp.oauth.browser_failed"))
		fmt.Println(authURL)
	} else {
		fmt.Println(i18n.T("mcp.oauth.browser_hint"))
		fmt.Println(authURL)
	}
	// Authorization servers route the browser by its existing session cookies:
	// AWS signin, for one, sends a browser with a live IAM session down a
	// different path that answers 400 for this flow, while an anonymous browser
	// authorizes fine. Nothing in the request distinguishes the two, so the
	// only recovery is on the human's side — say so up front instead of
	// waiting silently while the user stares at the provider's error page.
	fmt.Println(i18n.T("mcp.oauth.stale_session_hint"))
	fmt.Println(i18n.T("mcp.oauth.waiting"))

	timeoutTimer := time.NewTimer(mcpOAuthCallbackTimeout)
	defer timeoutTimer.Stop()

	select {
	case code = <-codeCh:
		return code, pkce.Verifier, nil
	case cbErr := <-errCh:
		return "", "", fmt.Errorf("%s: %w", i18n.T("mcp.oauth.callback_failed"), cbErr)
	case <-timeoutTimer.C:
		return "", "", callbackTimeoutError(mcpOAuthCallbackTimeout)
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
}

// callbackTimeoutError builds the "nobody completed the flow" error. The most
// common cause is the provider refusing the authorization page under the
// browser's existing session, so the recovery hint travels with the error too:
// the printed hint scrolls away while the user is off in the browser, and this
// error is what the model relays back to them.
func callbackTimeoutError(d time.Duration) error {
	return fmt.Errorf("%s — %s", i18n.T("mcp.oauth.callback_timeout", d), i18n.T("mcp.oauth.stale_session_hint"))
}

func writeCallbackHTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprint(w, body)
}
