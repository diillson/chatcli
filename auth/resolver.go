package auth

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/diillson/chatcli/i18n"
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

// ResolveAuthProvider resolves credentials for the given provider and returns a
// TokenProvider that refreshes OAuth tokens transparently. Lookup order:
//  1. Auth-profiles store (first match by provider).
//  2. Environment variables.
//
// The returned provider is alive: callers that retain it across requests get
// proactive background refresh and refresh-on-401 behavior automatically.
// Caller is responsible for calling Close() when discarding the provider
// (typically the LLMManager owns and closes them).
func ResolveAuthProvider(ctx context.Context, provider ProviderID, logger *zap.Logger) (TokenProvider, error) {
	store := LoadStore(logger)
	for id, c := range store.Profiles {
		if c == nil {
			continue
		}
		if !isProviderMatch(c.Provider, provider) {
			continue
		}
		switch c.CredType {
		case CredentialOAuth:
			tp := newOAuthTokenProvider(c, id, "auth-store", logger)
			return tp, nil
		case CredentialToken:
			key := strings.TrimSpace(c.Token)
			if key == "" {
				continue
			}
			return &staticTokenProvider{
				token:     key,
				mode:      AuthModeToken,
				provider:  provider,
				profileID: id,
				source:    "auth-store",
				email:     c.Email,
			}, nil
		case CredentialAPIKey:
			key := strings.TrimSpace(c.Key)
			if key == "" {
				continue
			}
			return &staticTokenProvider{
				token:     key,
				mode:      AuthModeAPIKey,
				provider:  provider,
				profileID: id,
				source:    "auth-store",
				email:     c.Email,
			}, nil
		}
	}

	if tp := envFallbackProvider(provider); tp != nil {
		return tp, nil
	}

	return nil, fmt.Errorf("%s", i18n.T("auth.resolver.no_credentials", provider))
}

func envFallbackProvider(provider ProviderID) TokenProvider {
	switch provider {
	case ProviderAnthropic:
		if v := strings.TrimSpace(os.Getenv("ANTHROPIC_OAUTH_TOKEN")); v != "" {
			return &staticTokenProvider{token: v, mode: AuthModeOAuth, provider: provider, source: "env:ANTHROPIC_OAUTH_TOKEN"}
		}
		if v := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); v != "" {
			return &staticTokenProvider{token: v, mode: AuthModeAPIKey, provider: provider, source: "env:ANTHROPIC_API_KEY"}
		}
	case ProviderOpenAI, ProviderOpenAICodex:
		if v := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); v != "" {
			return &staticTokenProvider{token: v, mode: AuthModeAPIKey, provider: provider, source: "env:OPENAI_API_KEY"}
		}
	case ProviderGitHubCopilot:
		if v := strings.TrimSpace(os.Getenv("GITHUB_COPILOT_TOKEN")); v != "" {
			return &staticTokenProvider{token: v, mode: AuthModeToken, provider: provider, source: "env:GITHUB_COPILOT_TOKEN"}
		}
	case ProviderGitHubModels:
		for _, envKey := range []string{"GITHUB_TOKEN", "GH_TOKEN", "GITHUB_MODELS_TOKEN"} {
			if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
				return &staticTokenProvider{token: v, mode: AuthModeToken, provider: provider, source: "env:" + envKey}
			}
		}
	}
	return nil
}

// ResolveAuth retains the legacy contract: it returns a prefixed-string
// representation ("oauth:...", "token:...", "apikey:...") plus metadata.
// New code should call ResolveAuthProvider directly. This wrapper exists for
// callers that forward the credential across a process boundary (remote/server
// mode) where a live TokenProvider cannot be used.
//
// The returned key is a one-shot snapshot: it is the current access token at
// resolution time. No background refresh occurs.
func ResolveAuth(ctx context.Context, provider ProviderID, logger *zap.Logger) (*ResolvedAuth, error) {
	tp, err := ResolveAuthProvider(ctx, provider, logger)
	if err != nil {
		return nil, err
	}
	// Snapshot the token. For OAuth providers this may trigger an in-place
	// refresh when the credential is expired, matching the previous behavior.
	token, err := tp.Token(ctx)
	tp.Close()
	if err != nil {
		return nil, err
	}

	prefix := ""
	switch tp.Mode() {
	case AuthModeOAuth:
		prefix = "oauth:"
	case AuthModeToken:
		prefix = "token:"
	case AuthModeAPIKey:
		prefix = "apikey:"
	}

	return &ResolvedAuth{
		APIKey:    prefix + token,
		ProfileID: tp.ProfileID(),
		Source:    tp.Source(),
		Mode:      tp.Mode(),
		Provider:  provider,
		Email:     tp.Email(),
	}, nil
}
