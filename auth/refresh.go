package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// RefreshOAuth refreshes an OAuth credential in-place and returns it.
// Supported: anthropic, openai-codex.
func RefreshOAuth(ctx context.Context, cred *AuthProfileCredential, logger *zap.Logger) (*AuthProfileCredential, error) {
	if cred == nil {
		return nil, fmt.Errorf("%s", i18n.T("auth.refresh.nil_cred"))
	}
	if cred.CredType != CredentialOAuth {
		return nil, fmt.Errorf("%s", i18n.T("auth.refresh.not_oauth"))
	}
	refresh := strings.TrimSpace(cred.Refresh)
	if refresh == "" {
		return nil, fmt.Errorf("%s", i18n.T("auth.refresh.missing_refresh"))
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
		return nil, fmt.Errorf("%s", i18n.T("auth.refresh.unsupported_provider", cred.Provider))
	}

	var tr *OAuthTokenResponse
	var err error

	if cred.Provider == ProviderAnthropic {
		// Use plain HTTP client for Anthropic to avoid Cloudflare issues.
		payload := map[string]any{
			"grant_type":    "refresh_token",
			"client_id":     clientID,
			"refresh_token": refresh,
		}
		tr, err = exchangeAnthropicToken(ctx, tokenURL, payload)
		// Fallback: refresh tokens issued by the legacy console.anthropic.com
		// endpoint may not be accepted by platform.claude.com yet. Try legacy
		// endpoint if the primary one returns auth/invalid-request errors.
		if err != nil && tokenURL != AnthropicTokenURLLegacy {
			logger.Warn("anthropic refresh failed on primary endpoint, retrying on legacy",
				zap.String("primary", tokenURL),
				zap.Error(err),
			)
			tr, err = exchangeAnthropicToken(ctx, AnthropicTokenURLLegacy, payload)
		}
	} else {
		tr, err = exchangeOAuthToken(ctx, logger, tokenURL, map[string]any{
			"grant_type":    "refresh_token",
			"client_id":     clientID,
			"refresh_token": refresh,
		})
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.refresh.failed"), err)
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
