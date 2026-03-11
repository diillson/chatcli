package auth

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// RefreshOAuth refreshes an OAuth credential in-place and returns it.
// Supported: anthropic, openai-codex.
func RefreshOAuth(ctx context.Context, cred *AuthProfileCredential, logger *zap.Logger) (*AuthProfileCredential, error) {
	if cred == nil {
		return nil, fmt.Errorf("nil cred")
	}
	if cred.CredType != CredentialOAuth {
		return nil, fmt.Errorf("cred is not oauth")
	}
	refresh := strings.TrimSpace(cred.Refresh)
	if refresh == "" {
		return nil, fmt.Errorf("missing refresh token")
	}

	var tokenURL string
	var clientID string
	switch cred.Provider {
	case ProviderAnthropic:
		tokenURL = AnthropicTokenURL
		clientID = cred.ClientID
		if clientID == "" {
			clientID = AnthropicOAuthClientID
		}
	case ProviderOpenAICodex:
		tokenURL = OpenAITokenURL
		clientID = cred.ClientID
		if clientID == "" {
			clientID = OpenAICodexClientID()
		}
	default:
		return nil, fmt.Errorf("unsupported oauth provider: %s", cred.Provider)
	}

	var tr *OAuthTokenResponse
	var err error

	if cred.Provider == ProviderAnthropic {
		// Use plain HTTP client for Anthropic to avoid Cloudflare issues
		tr, err = exchangeAnthropicToken(ctx, tokenURL, map[string]any{
			"grant_type":    "refresh_token",
			"client_id":     clientID,
			"refresh_token": refresh,
		})
	} else {
		tr, err = exchangeOAuthToken(ctx, logger, tokenURL, map[string]any{
			"grant_type":    "refresh_token",
			"client_id":     clientID,
			"refresh_token": refresh,
		})
	}
	if err != nil {
		return nil, fmt.Errorf("oauth refresh failed: %w", err)
	}

	if tr.AccessToken != "" {
		cred.Access = tr.AccessToken
	}
	if tr.RefreshToken != "" {
		cred.Refresh = tr.RefreshToken
	}
	if tr.ExpiresIn > 0 {
		// M6: Use calcExpiresAtMilli for consistent 5-minute safety margin
		cred.Expires = calcExpiresAtMilli(tr.ExpiresIn)
	}
	return cred, nil
}
