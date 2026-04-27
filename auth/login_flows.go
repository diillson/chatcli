package auth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

const (
	// Anthropic (Claude) — localhost callback flow (default).
	// The Claude Code CLI uses an ephemeral local HTTP server on http://localhost:<port>/callback.
	// The legacy paste flow (console.anthropic.com) is kept as a fallback when the local
	// listener cannot bind, or when CHATCLI_ANTHROPIC_LEGACY_OAUTH=1 is set.
	AnthropicOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	AnthropicAuthURL       = "https://claude.com/cai/oauth/authorize"
	AnthropicTokenURL      = "https://platform.claude.com/v1/oauth/token" //#nosec G101 -- public OAuth endpoint URL, not a credential
	AnthropicScopes        = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	AnthropicCallbackPath  = "/callback"
	AnthropicProfileURL    = "https://api.anthropic.com/api/oauth/profile" //#nosec G101 -- public OAuth profile endpoint
	// AnthropicSuccessRedirectURL is the branded post-login page Claude Code redirects
	// the browser to after the loopback callback receives the code. Mirroring it gives
	// users a familiar landing page instead of a blank localhost response.
	AnthropicSuccessRedirectURL = "https://platform.claude.com/oauth/code/success?app=claude-code"

	// Legacy paste-mode constants (fallback only).
	AnthropicAuthURLLegacy     = "https://claude.ai/oauth/authorize"
	AnthropicTokenURLLegacy    = "https://console.anthropic.com/v1/oauth/token"      //#nosec G101 -- public OAuth endpoint URL, not a credential
	AnthropicRedirectURILegacy = "https://console.anthropic.com/oauth/code/callback" //#nosec G101 -- public OAuth redirect URI
	AnthropicLegacyScopes      = "org:create_api_key user:profile user:inference"

	// OpenAI Codex OAuth
	defaultOpenAICodexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	OpenAIAuthURL              = "https://auth.openai.com/oauth/authorize"
	OpenAITokenURL             = "https://auth.openai.com/oauth/token" //#nosec G101 -- public OAuth endpoint URL
	OpenAIRedirectURI          = "http://localhost:1455/auth/callback"
	OpenAIScopes               = "openid profile email offline_access"
	OpenAICallbackPort         = "1455"

	// oauthCallbackTimeout is the maximum time to wait for an OAuth callback.
	oauthCallbackTimeout = 5 * time.Minute
)

// OpenAICodexClientID returns the OpenAI Codex client ID, allowing override via env var.
func OpenAICodexClientID() string {
	if id := os.Getenv("CHATCLI_OPENAI_CLIENT_ID"); id != "" {
		return id
	}
	return defaultOpenAICodexClientID
}

// LoginAnthropicOAuth authenticates via OAuth with Anthropic.
//
// Default flow uses an ephemeral local HTTP server on http://localhost:<port>/callback
// to receive the authorization code automatically (mirrors Claude Code's behavior).
//
// Fallback paste flow (legacy console.anthropic.com redirect) is used when:
//   - the local listener cannot bind (port blocked, sandboxed env), or
//   - CHATCLI_ANTHROPIC_LEGACY_OAUTH=1 is set.
func LoginAnthropicOAuth(ctx context.Context, logger *zap.Logger) (profileID string, err error) {
	if os.Getenv("CHATCLI_ANTHROPIC_LEGACY_OAUTH") == "1" {
		return loginAnthropicLegacyPaste(ctx, logger)
	}

	// Try localhost callback flow; fall back to paste if listener fails.
	id, err := loginAnthropicLocalhost(ctx, logger)
	if err == nil {
		return id, nil
	}
	if !isListenerBindError(err) {
		return "", err
	}
	logger.Warn("anthropic localhost callback failed, falling back to paste mode", zap.Error(err))
	fmt.Println(i18n.T("auth.login.anthropic_fallback_paste"))
	return loginAnthropicLegacyPaste(ctx, logger)
}

// loginAnthropicLocalhost runs the localhost callback flow.
func loginAnthropicLocalhost(ctx context.Context, logger *zap.Logger) (string, error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return "", err
	}
	state, err := GenerateState()
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("auth.login.anthropic_state_failed"), err)
	}

	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return "", listenerBindErr(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://localhost:%d%s", port, AnthropicCallbackPath)

	q := url.Values{}
	q.Set("code", "true")
	q.Set("client_id", AnthropicOAuthClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", AnthropicScopes)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	authURL := AnthropicAuthURL + "?" + q.Encode()

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	srv := &http.Server{
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       oauthCallbackTimeout,
	}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("Cache-Control", "no-store")

		if r.URL.Path != AnthropicCallbackPath {
			http.NotFound(w, r)
			return
		}
		receivedState := r.URL.Query().Get("state")
		if receivedState != state {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, i18n.T("auth.login.anthropic_auth_failed_html"))
			errCh <- fmt.Errorf("%s", i18n.T("auth.login.anthropic_csrf_error"))
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, i18n.T("auth.login.anthropic_no_code_html"))
			errCh <- fmt.Errorf("%s", i18n.T("auth.login.anthropic_no_code_error"))
			return
		}
		// Redirect to Anthropic's branded success page (matches Claude Code).
		http.Redirect(w, r, AnthropicSuccessRedirectURL, http.StatusFound)
		codeCh <- code
	})

	go func() {
		if serveErr := srv.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
		}
	}()

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Println(i18n.T("auth.login.anthropic_opening"))
	if openErr := openBrowser(authURL); openErr != nil {
		fmt.Println(i18n.T("auth.login.anthropic_browser_failed"))
		fmt.Println(authURL)
	} else {
		fmt.Println(i18n.T("auth.login.anthropic_browser_ok_callback"))
		fmt.Println(i18n.T("auth.login.anthropic_browser_hint"))
		fmt.Println(authURL)
	}
	fmt.Println(i18n.T("auth.login.anthropic_waiting"))

	timeoutTimer := time.NewTimer(oauthCallbackTimeout)
	defer timeoutTimer.Stop()

	var code string
	select {
	case code = <-codeCh:
	case cbErr := <-errCh:
		return "", fmt.Errorf("%s: %w", i18n.T("auth.login.anthropic_callback_error"), cbErr)
	case <-timeoutTimer.C:
		return "", fmt.Errorf("%s", i18n.T("auth.login.anthropic_timeout", oauthCallbackTimeout))
	case <-ctx.Done():
		return "", ctx.Err()
	}

	tr, err := exchangeAnthropicToken(ctx, AnthropicTokenURL, map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     AnthropicOAuthClientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  redirectURI,
		"code_verifier": pkce.Verifier,
	})
	if err != nil {
		return "", err
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("OAuth token exchange returned empty access token")
	}
	if tr.ExpiresIn <= 0 {
		logger.Warn("OAuth token has no expiry set, using default 3600s")
		tr.ExpiresIn = 3600
	}

	email := fetchAnthropicEmail(ctx, tr.AccessToken, logger)

	profileID := "anthropic:default"
	cred := &AuthProfileCredential{
		CredType: CredentialOAuth,
		Provider: ProviderAnthropic,
		Access:   tr.AccessToken,
		Refresh:  tr.RefreshToken,
		Expires:  calcExpiresAtMilli(tr.ExpiresIn),
		ClientID: AnthropicOAuthClientID,
		Email:    email,
	}
	if err := UpsertProfile(profileID, cred, logger); err != nil {
		return "", err
	}
	return profileID, nil
}

// loginAnthropicLegacyPaste runs the legacy paste flow (console.anthropic.com redirect).
func loginAnthropicLegacyPaste(ctx context.Context, logger *zap.Logger) (string, error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return "", err
	}

	q := url.Values{}
	q.Set("code", "true")
	q.Set("client_id", AnthropicOAuthClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", AnthropicRedirectURILegacy)
	q.Set("scope", AnthropicLegacyScopes)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", pkce.Verifier)
	authURL := AnthropicAuthURLLegacy + "?" + q.Encode()

	fmt.Println(i18n.T("auth.login.anthropic_opening"))
	if openErr := openBrowser(authURL); openErr != nil {
		fmt.Println(i18n.T("auth.login.browser_failed"))
	} else {
		fmt.Println(i18n.T("auth.login.anthropic_browser_ok"))
		fmt.Println(i18n.T("auth.login.anthropic_browser_hint"))
	}
	fmt.Println(authURL)

	if runtime.GOOS != "windows" {
		cmd := exec.Command("stty", "sane")
		cmd.Stdin = os.Stdin
		_ = cmd.Run()
	}

	var rawCode string
	fmt.Print(i18n.T("auth.login.anthropic_paste_code"))
	reader := bufio.NewReader(os.Stdin)
	rawCode, err = reader.ReadString('\n')
	if err != nil && rawCode == "" {
		return "", fmt.Errorf("%s: %w", i18n.T("auth.login.anthropic_read_failed"), err)
	}
	rawCode = strings.TrimSpace(rawCode)

	var code, state string
	if idx := strings.Index(rawCode, "#"); idx != -1 {
		code = rawCode[:idx]
		state = rawCode[idx+1:]
	} else {
		code = rawCode
	}
	if code == "" {
		return "", fmt.Errorf("%s", i18n.T("auth.login.anthropic_code_required"))
	}

	tr, err := exchangeAnthropicToken(ctx, AnthropicTokenURLLegacy, map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     AnthropicOAuthClientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  AnthropicRedirectURILegacy,
		"code_verifier": pkce.Verifier,
	})
	if err != nil {
		return "", err
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("OAuth token exchange returned empty access token")
	}
	if tr.ExpiresIn <= 0 {
		logger.Warn("OAuth token has no expiry set, using default 3600s")
		tr.ExpiresIn = 3600
	}

	email := fetchAnthropicEmail(ctx, tr.AccessToken, logger)

	profileID := "anthropic:default"
	cred := &AuthProfileCredential{
		CredType: CredentialOAuth,
		Provider: ProviderAnthropic,
		Access:   tr.AccessToken,
		Refresh:  tr.RefreshToken,
		Expires:  calcExpiresAtMilli(tr.ExpiresIn),
		ClientID: AnthropicOAuthClientID,
		Email:    email,
	}
	if err := UpsertProfile(profileID, cred, logger); err != nil {
		return "", err
	}
	return profileID, nil
}

// listenerBindErr wraps an error indicating the local listener could not bind,
// so callers can detect it via isListenerBindError and fall back to paste mode.
type listenerBindError struct{ err error }

func (e *listenerBindError) Error() string { return e.err.Error() }
func (e *listenerBindError) Unwrap() error { return e.err }

func listenerBindErr(err error) error { return &listenerBindError{err: err} }

func isListenerBindError(err error) bool {
	var lbe *listenerBindError
	return errors.As(err, &lbe)
}

func LoginOpenAICodexOAuth(ctx context.Context, logger *zap.Logger) (profileID string, err error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return "", err
	}
	state, err := GenerateState()
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("auth.login.openai_state_failed"), err)
	}

	clientID := OpenAICodexClientID() // M5: supports env override

	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", OpenAIRedirectURI)
	q.Set("scope", OpenAIScopes)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("id_token_add_organizations", "true")
	q.Set("codex_cli_simplified_flow", "true")
	authURL := OpenAIAuthURL + "?" + q.Encode()

	// Start local HTTP server to capture the OAuth callback
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	listener, err := net.Listen("tcp", "localhost:"+OpenAICallbackPort)
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("auth.login.openai_listener_failed", OpenAICallbackPort), err)
	}

	srv := &http.Server{
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       oauthCallbackTimeout, // M2: idle timeout
	}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Security headers for all responses
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("Cache-Control", "no-store")

		if r.URL.Path != "/auth/callback" {
			http.NotFound(w, r)
			return
		}
		// H2: Validate state parameter to prevent CSRF
		receivedState := r.URL.Query().Get("state")
		if receivedState != state {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, i18n.T("auth.login.openai_auth_failed_html"))
			errCh <- fmt.Errorf("%s", i18n.T("auth.login.openai_csrf_error"))
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, i18n.T("auth.login.openai_no_code_html"))
			errCh <- fmt.Errorf("%s", i18n.T("auth.login.openai_no_code_error"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, i18n.T("auth.login.openai_success_html"))
		codeCh <- code
	})

	go func() {
		if serveErr := srv.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
		}
	}()

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	// Open browser automatically
	fmt.Println(i18n.T("auth.login.openai_opening"))
	if openErr := openBrowser(authURL); openErr != nil {
		fmt.Println(i18n.T("auth.login.openai_browser_failed"))
		fmt.Println(authURL)
	} else {
		fmt.Println(i18n.T("auth.login.openai_browser_ok"))
		fmt.Println(i18n.T("auth.login.openai_browser_hint"))
		fmt.Println(authURL)
	}

	fmt.Println(i18n.T("auth.login.openai_waiting"))

	// M2: Add global timeout so port doesn't stay open indefinitely
	timeoutTimer := time.NewTimer(oauthCallbackTimeout)
	defer timeoutTimer.Stop()

	// Wait for callback, context cancellation, or timeout
	var code string
	select {
	case code = <-codeCh:
		// got it
	case cbErr := <-errCh:
		return "", fmt.Errorf("%s: %w", i18n.T("auth.login.openai_callback_error"), cbErr)
	case <-timeoutTimer.C:
		return "", fmt.Errorf("%s", i18n.T("auth.login.openai_timeout", oauthCallbackTimeout))
	case <-ctx.Done():
		return "", ctx.Err()
	}

	tr, err := exchangeOAuthToken(ctx, logger, OpenAITokenURL, map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     clientID,
		"code":          code,
		"redirect_uri":  OpenAIRedirectURI,
		"code_verifier": pkce.Verifier,
	})

	if err != nil {
		return "", err
	}
	profileID = "openai-codex:default"
	cred := &AuthProfileCredential{
		CredType: CredentialOAuth,
		Provider: ProviderOpenAICodex,
		Access:   tr.AccessToken,
		Refresh:  tr.RefreshToken,
		Expires:  calcExpiresAtMilli(tr.ExpiresIn),
		ClientID: clientID,
		Email:    "",
	}
	if err := UpsertProfile(profileID, cred, logger); err != nil {
		return "", err
	}
	return profileID, nil
}

// openBrowser opens a URL in the default browser.
func openBrowser(rawURL string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", rawURL).Start() //#nosec G204 -- agent/CLI tool execution; commands validated by command_validator + policy_manager upstream
	case "linux":
		return exec.Command("xdg-open", rawURL).Start() //#nosec G204 -- agent/CLI tool execution; commands validated by command_validator + policy_manager upstream
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start() //#nosec G204 -- agent/CLI tool execution; commands validated by command_validator + policy_manager upstream
	default:
		return fmt.Errorf("%s", i18n.T("auth.login.unsupported_platform"))
	}
}

func FormatAuthStatus(logger *zap.Logger) string {
	store := LoadStore(logger)
	a := store.Profiles["anthropic:default"]
	o := store.Profiles["openai-codex:default"]
	g := store.Profiles["github-copilot:default"]
	gm := store.Profiles["github-models:default"]
	fmtAuth := func(c *AuthProfileCredential) string {
		if c == nil {
			return i18n.T("auth.login.status_not_connected")
		}
		if c.Expires == 0 {
			return i18n.T("auth.login.status_no_expiry", c.CredType)
		}
		return i18n.T("auth.login.status_format", c.CredType, FormatExpiry(c.Expires))
	}
	return i18n.T("auth.login.status_display", fmtAuth(a), fmtAuth(o), fmtAuth(g), fmtAuth(gm))
}

func Logout(provider ProviderID, logger *zap.Logger) error {
	store := LoadStore(logger)
	for id, c := range store.Profiles {
		if c != nil && c.Provider == provider {
			delete(store.Profiles, id)
		}
	}
	return SaveStore(store, logger)
}
