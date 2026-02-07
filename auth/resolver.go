package auth

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
)

// isProviderMatch returns true if the credential's provider is a valid match
// for the requested provider. This handles the case where OpenAI OAuth tokens
// are stored under ProviderOpenAICodex but should be found when resolving ProviderOpenAI.
func isProviderMatch(credProvider, requestedProvider ProviderID) bool {
	if credProvider == requestedProvider {
		return true
	}
	if requestedProvider == ProviderOpenAI && credProvider == ProviderOpenAICodex {
		return true
	}
	return false
}

// ResolveAuth resolves a chave a usar (um apikey OU access token) para um provedor.
// Ordem:
// 1) auth-profiles store (first match for provider)
// 2) env vars
//
// Nota: para manter backward compatibility com seus clients atuais,
// retornamos uma string "oauth:eaxxxx" quando for token OAuth,
// e uma string "apikey:exxxxx" quando for API key.
func ResolveAuth(ctx context.Context, provider ProviderID, logger *zap.Logger) (*ResolvedAuth, error) {
	// Best-effort sync external CLI creds on load
	// (sync disabled: cli_sync.go fix pending)

	store := LoadStore(logger)
	for id, c := range store.Profiles {
		if c == nil {
			continue
		}
		if !isProviderMatch(c.Provider, provider) {
			continue
		}
		if c.CredType == CredentialOAuth {
			if c.IsExpired() {
				_, _ = RefreshOAuth(ctx, c, logger)
				_ = SaveStore(store, logger)
			}
			key := strings.TrimSpace(c.Access)
			if key != "" {
				return &ResolvedAuth{
					APIKey:    "oauth:" + key,
					ProfileID: id,
					Source:    "auth-store",
					Mode:      AuthModeOAuth,
					Provider:  provider,
					Email:     c.Email,
				}, nil
			}
		}
		if c.CredType == CredentialToken {
			key := strings.TrimSpace(c.Token)
			if key != "" {
				return &ResolvedAuth{
					APIKey:    "token:" + key,
					ProfileID: id,
					Source:    "auth-store",
					Mode:      AuthModeToken,
					Provider:  provider,
					Email:     c.Email,
				}, nil
			}
		}
		if c.CredType == CredentialAPIKey {
			key := strings.TrimSpace(c.Key)
			if key != "" {
				return &ResolvedAuth{
					APIKey:    "apikey:" + key,
					ProfileID: id,
					Source:    "auth-store",
					Mode:      AuthModeAPIKey,
					Provider:  provider,
					Email:     c.Email,
				}, nil
			}
		}
	}

	// env fallback
	switch provider {
	case ProviderAnthropic:
		if v := strings.TrimSpace(os.Getenv("ANTHROPIC_OAUTH_TOKEN")); v != "" {
			return &ResolvedAuth{APIKey: "oauth:" + v, Source: "env:ANTHROPIC_OAUTH_TOKEN", Mode: AuthModeOAuth, Provider: provider}, nil
		}
		if v := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); v != "" {
			return &ResolvedAuth{APIKey: "apikey:" + v, Source: "env:ANTHROPIC_API_KEY", Mode: AuthModeAPIKey, Provider: provider}, nil
		}

	case ProviderOpenAI, ProviderOpenAICodex:
		if v := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); v != "" {
			return &ResolvedAuth{APIKey: "apikey:" + v, Source: "env:OPENAI_API_KEY", Mode: AuthModeAPIKey, Provider: provider}, nil
		}
	}

	return nil, fmt.Errorf("no credentials for provider %s", provider)
}
