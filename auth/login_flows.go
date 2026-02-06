package auth

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"go.uber.org/zap"
)

const (
	// Anthropic (Claude)
	AnthropicOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	AnthropicAuthURL       = "https://claude.ai/oauth/authorize"
	AnthropicTokenURL      = "https://console.anthropic.com/v1/oauth/token"
	AnthropicRedirectURI   = "https://console.anthropic.com/oauth/code/callback"
	AnthropicScopes        = "org:create_api_key user:profile user:inference"

	// OpenAI Codex OAuth
	OpenAICodexClientID = "013696bc-5381-4baa-809e-70df20e4974e"
	OpenAIAuthURL       = "https://auth.openai.com/oauth/authorize"
	OpenAITokenURL      = "https://auth.openai.com/oauth/token"
	OpenAIRedirectURI   = "http://127.0.0.1:1455/auth/callback"
	OpenAIScopes        = "openid profile email offline_access"
	OpenAIAudience      = "https://api.openai.com/v1"
)

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

	fmt.Println("\nAUTH [Anthropic] Open this URL in your browser, then paste the code: ")
	fmt.Println(authURL)

	cmd := exec.Command("stty", "sane")
	cmd.Stdin = os.Stdin
	_ = cmd.Run()

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
		"redirect_uri":  AnthropicRedirectURI,
		"code_verifier": pkce.Verifier,
		"state":         pkce.Verifier,
	})
	if err != nil {
		return "", err
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
	state, _ := GenerateState()

	q := url.Values{}
	q.Set("client_id", OpenAICodexClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", OpenAIRedirectURI)
	q.Set("scope", OpenAIScopes)
	q.Set("audience", OpenAIAudience)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	authURL := OpenAIAuthURL + "?" + q.Encode()

	fmt.Println("\nAUTH [OpenAI Codex] Open this URL in your browser, then paste the redirect url here:")
	fmt.Println(authURL)

	cmd := exec.Command("stty", "sane")
	cmd.Stdin = os.Stdin
	_ = cmd.Run()

	var redirect string
	fmt.Print("> redirect url: ")
	//_, _ = fmt.Scan(&redirect)
	reader := bufio.NewReader(os.Stdin)
	redirect, _ = reader.ReadString('\n')
	redirect = strings.TrimSpace(redirect)
	if redirect == "" {
		return "", fmt.Errorf("redirect url is required")
	}
	u, err := url.Parse(redirect)
	if err != nil {
		return "", fmt.Errorf("bad redirect url: %w", err)
	}
	code := u.Query().Get("code")
	if strings.TrimSpace(code) == "" {
		return "", fmt.Errorf("code not found in redirect url")
	}

	tr, err := exchangeOAuthToken(ctx, logger, OpenAITokenURL, map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     OpenAICodexClientID,
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
		ClientID: OpenAICodexClientID,
		Email:    "",
	}
	if err := UpsertProfile(profileID, cred, logger); err != nil {
		return "", err
	}
	return profileID, nil
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
