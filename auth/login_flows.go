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

	"go.uber.org/zap"
)

const (
	// Anthropic (Claude)
	AnthropicOAuthClientID       = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	AnthropicAuthURL             = "https://claude.ai/oauth/authorize"
	AnthropicTokenURL            = "https://console.anthropic.com/v1/oauth/token"
	AnthropicCallbackPort        = "1456"
	AnthropicLocalRedirectURI    = "http://localhost:1456/auth/callback"
	AnthropicFallbackRedirectURI = "https://console.anthropic.com/oauth/code/callback"
	AnthropicScopes              = "user:profile user:inference" // L6: removed overly broad org:create_api_key

	// OpenAI Codex OAuth
	defaultOpenAICodexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	OpenAIAuthURL              = "https://auth.openai.com/oauth/authorize"
	OpenAITokenURL             = "https://auth.openai.com/oauth/token"
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

func LoginAnthropicOAuth(ctx context.Context, logger *zap.Logger) (profileID string, err error) {
	// CHATCLI_ANTHROPIC_MANUAL_AUTH=true falls back to manual code paste
	if strings.EqualFold(os.Getenv("CHATCLI_ANTHROPIC_MANUAL_AUTH"), "true") {
		return loginAnthropicManual(ctx, logger)
	}
	return loginAnthropicCallback(ctx, logger)
}

// loginAnthropicCallback uses a local HTTP server to capture the OAuth callback automatically.
func loginAnthropicCallback(ctx context.Context, logger *zap.Logger) (string, error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return "", err
	}
	state, err := GenerateState()
	if err != nil {
		return "", fmt.Errorf("failed to generate OAuth state: %w", err)
	}

	redirectURI := AnthropicLocalRedirectURI

	q := url.Values{}
	q.Set("client_id", AnthropicOAuthClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", AnthropicScopes)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	authURL := AnthropicAuthURL + "?" + q.Encode()

	// Start local HTTP server to capture the OAuth callback
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	listener, err := net.Listen("tcp", "localhost:"+AnthropicCallbackPort)
	if err != nil {
		logger.Warn("Failed to start local callback server, falling back to manual auth",
			zap.String("port", AnthropicCallbackPort), zap.Error(err))
		return loginAnthropicManual(ctx, logger)
	}

	srv := &http.Server{
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       oauthCallbackTimeout,
	}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("Cache-Control", "no-store")

		if r.URL.Path != "/auth/callback" {
			http.NotFound(w, r)
			return
		}
		receivedState := r.URL.Query().Get("state")
		if receivedState != state {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "<html><body><h2>Authentication failed</h2><p>Invalid state parameter (possible CSRF attack). You can close this tab.</p></body></html>")
			errCh <- fmt.Errorf("OAuth state mismatch: possible CSRF attack")
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "<html><body><h2>Authentication failed</h2><p>No authorization code received. You can close this tab.</p></body></html>")
			errCh <- fmt.Errorf("no code in callback")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><body><h2>Authentication successful!</h2><p>You can close this tab and return to the terminal.</p></body></html>")
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
	fmt.Println("\nAUTH [Anthropic] Opening browser for authentication...")
	if openErr := openBrowser(authURL); openErr != nil {
		fmt.Println("Could not open browser automatically. Open this URL manually:")
		fmt.Println(authURL)
	} else {
		fmt.Println("Browser opened. Waiting for authentication callback...")
		fmt.Println("If the browser did not open, copy and paste this URL:")
		fmt.Println(authURL)
	}

	fmt.Println("\nWaiting for callback (press Ctrl+C to cancel, auto-timeout in 5 minutes)...")
	fmt.Println("TIP: If callback fails, set CHATCLI_ANTHROPIC_MANUAL_AUTH=true to use manual code paste.")

	timeoutTimer := time.NewTimer(oauthCallbackTimeout)
	defer timeoutTimer.Stop()

	var code string
	select {
	case code = <-codeCh:
		// got it
	case cbErr := <-errCh:
		return "", fmt.Errorf("callback error: %w", cbErr)
	case <-timeoutTimer.C:
		return "", fmt.Errorf("OAuth callback timed out after %s", oauthCallbackTimeout)
	case <-ctx.Done():
		return "", ctx.Err()
	}

	tr, err := exchangeOAuthToken(ctx, logger, AnthropicTokenURL, map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     AnthropicOAuthClientID,
		"code":          code,
		"redirect_uri":  redirectURI,
		"code_verifier": pkce.Verifier,
		"state":         state,
	})
	if err != nil {
		return "", err
	}
	profileID := "anthropic:default"
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

// loginAnthropicManual is the fallback flow where the user manually copies the code from the browser.
func loginAnthropicManual(ctx context.Context, logger *zap.Logger) (string, error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return "", err
	}
	state, err := GenerateState()
	if err != nil {
		return "", fmt.Errorf("failed to generate OAuth state: %w", err)
	}

	q := url.Values{}
	q.Set("code", "true")
	q.Set("client_id", AnthropicOAuthClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", AnthropicFallbackRedirectURI)
	q.Set("scope", AnthropicScopes)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	authURL := AnthropicAuthURL + "?" + q.Encode()

	fmt.Println("\nAUTH [Anthropic] Open this URL in your browser, then paste the code: ")
	fmt.Println(authURL)

	// L5: Only run stty on Unix systems
	if runtime.GOOS != "windows" {
		cmd := exec.Command("stty", "sane")
		cmd.Stdin = os.Stdin
		_ = cmd.Run()
	}

	var code string
	fmt.Print("> code: ")
	reader := bufio.NewReader(os.Stdin)
	code, _ = reader.ReadString('\n')
	code = strings.TrimSpace(code)
	if idx := strings.Index(code, "#"); idx != -1 {
		code = code[:idx]
	}
	if code == "" {
		return "", fmt.Errorf("code is required")
	}

	tr, err := exchangeOAuthToken(ctx, logger, AnthropicTokenURL, map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     AnthropicOAuthClientID,
		"code":          code,
		"redirect_uri":  AnthropicFallbackRedirectURI,
		"code_verifier": pkce.Verifier,
		"state":         state,
	})
	if err != nil {
		return "", err
	}
	profileID := "anthropic:default"
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
		return "", fmt.Errorf("failed to generate OAuth state: %w", err)
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
		return "", fmt.Errorf("failed to start local callback server on port %s: %w", OpenAICallbackPort, err)
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
			fmt.Fprint(w, "<html><body><h2>Authentication failed</h2><p>Invalid state parameter (possible CSRF attack). You can close this tab.</p></body></html>")
			errCh <- fmt.Errorf("OAuth state mismatch: possible CSRF attack")
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "<html><body><h2>Authentication failed</h2><p>No authorization code received. You can close this tab.</p></body></html>")
			errCh <- fmt.Errorf("no code in callback")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><body><h2>Authentication successful!</h2><p>You can close this tab and return to the terminal.</p></body></html>")
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
	fmt.Println("\nAUTH [OpenAI Codex] Opening browser for authentication...")
	if openErr := openBrowser(authURL); openErr != nil {
		fmt.Println("Could not open browser automatically. Open this URL manually:")
		fmt.Println(authURL)
	} else {
		fmt.Println("Browser opened. Waiting for authentication callback...")
		fmt.Println("If the browser did not open, copy and paste this URL:")
		fmt.Println(authURL)
	}

	fmt.Println("\nWaiting for callback (press Ctrl+C to cancel, auto-timeout in 5 minutes)...")

	// M2: Add global timeout so port doesn't stay open indefinitely
	timeoutTimer := time.NewTimer(oauthCallbackTimeout)
	defer timeoutTimer.Stop()

	// Wait for callback, context cancellation, or timeout
	var code string
	select {
	case code = <-codeCh:
		// got it
	case cbErr := <-errCh:
		return "", fmt.Errorf("callback error: %w", cbErr)
	case <-timeoutTimer.C:
		return "", fmt.Errorf("OAuth callback timed out after %s", oauthCallbackTimeout)
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
		return exec.Command("open", rawURL).Start()
	case "linux":
		return exec.Command("xdg-open", rawURL).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start()
	default:
		return fmt.Errorf("unsupported platform")
	}
}

func FormatAuthStatus(logger *zap.Logger) string {
	store := LoadStore(logger)
	a := store.Profiles["anthropic:default"]
	o := store.Profiles["openai-codex:default"]
	fmtAuth := func(c *AuthProfileCredential) string {
		if c == nil {
			return "not connected"
		}
		return fmt.Sprintf("type=%s expiry=%s", c.CredType, FormatExpiry(c.Expires))
	}
	return fmt.Sprintf("Anthropic: %s\nOpenAI Codex: %s", fmtAuth(a), fmtAuth(o))
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
