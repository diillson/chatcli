package auth

import (
	"bufio"
	"context"
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
	// Anthropic (Claude)
	// NOTE: Anthropic's OAuth does not support localhost redirect URIs.
	// The only registered redirect is console.anthropic.com which displays the code for the user to copy.
	AnthropicOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	AnthropicAuthURL       = "https://claude.ai/oauth/authorize"
	AnthropicTokenURL      = "https://console.anthropic.com/v1/oauth/token"      //#nosec G101 -- public OAuth endpoint URL, not a credential
	AnthropicRedirectURI   = "https://console.anthropic.com/oauth/code/callback" //#nosec G101 -- public OAuth redirect URI
	AnthropicScopes        = "org:create_api_key user:profile user:inference"

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
// Anthropic's OAuth does not support localhost redirect URIs, so the flow opens the browser
// and the user copies the authorization code displayed on the Anthropic console page.
func LoginAnthropicOAuth(ctx context.Context, logger *zap.Logger) (profileID string, err error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return "", err
	}

	q := url.Values{}
	q.Set("code", "true")
	q.Set("client_id", AnthropicOAuthClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", AnthropicRedirectURI)
	q.Set("scope", AnthropicScopes)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", pkce.Verifier)
	authURL := AnthropicAuthURL + "?" + q.Encode()

	// Try to open browser automatically for convenience
	fmt.Println(i18n.T("auth.login.anthropic_opening"))
	if openErr := openBrowser(authURL); openErr != nil {
		fmt.Println(i18n.T("auth.login.browser_failed"))
	} else {
		fmt.Println(i18n.T("auth.login.anthropic_browser_ok"))
		fmt.Println(i18n.T("auth.login.anthropic_browser_hint"))
	}
	fmt.Println(authURL)

	// L5: Only run stty on Unix systems
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

	tr, err := exchangeAnthropicToken(ctx, AnthropicTokenURL, map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     AnthropicOAuthClientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  AnthropicRedirectURI,
		"code_verifier": pkce.Verifier,
	})
	if err != nil {
		return "", err
	}
	// Security (M12): Validate OAuth response fields
	if tr.AccessToken == "" {
		return "", fmt.Errorf("OAuth token exchange returned empty access token")
	}
	if tr.ExpiresIn <= 0 {
		logger.Warn("OAuth token has no expiry set, using default 3600s")
		tr.ExpiresIn = 3600
	}

	profileID = "anthropic:default"
	cred := &AuthProfileCredential{
		CredType: CredentialOAuth,
		Provider: ProviderAnthropic,
		Access:   tr.AccessToken,
		Refresh:  tr.RefreshToken,
		Expires:  calcExpiresAtMilli(tr.ExpiresIn),
		ClientID: AnthropicOAuthClientID,
		Email:    "",
	}
	if err := UpsertProfile(profileID, cred, logger); err != nil {
		return "", err
	}
	return profileID, nil
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
			fmt.Fprint(w, i18n.T("auth.login.openai_auth_failed_html"))
			errCh <- fmt.Errorf("%s", i18n.T("auth.login.openai_csrf_error"))
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, i18n.T("auth.login.openai_no_code_html"))
			errCh <- fmt.Errorf("%s", i18n.T("auth.login.openai_no_code_error"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, i18n.T("auth.login.openai_success_html"))
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
