package auth

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

func init() {
	i18n.Init()
}

// withTempStore points the store path env var at a fresh tmpdir for the
// duration of the test, restoring the previous value at cleanup. Required
// because the store has a process-wide singleton cache.
func withTempStore(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	prev := os.Getenv("CHATCLI_AUTH_DIR")
	if err := os.Setenv("CHATCLI_AUTH_DIR", dir); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	InvalidateCache()
	t.Cleanup(func() {
		_ = os.Setenv("CHATCLI_AUTH_DIR", prev)
		InvalidateCache()
	})
}

func TestIsProviderMatch(t *testing.T) {
	cases := []struct {
		cred, want ProviderID
		ok         bool
	}{
		{ProviderAnthropic, ProviderAnthropic, true},
		{ProviderOpenAI, ProviderOpenAI, true},
		{ProviderOpenAICodex, ProviderOpenAI, true},
		{ProviderOpenAI, ProviderOpenAICodex, false},
		{ProviderAnthropic, ProviderOpenAI, false},
	}
	for _, c := range cases {
		if got := isProviderMatch(c.cred, c.want); got != c.ok {
			t.Errorf("isProviderMatch(%s,%s) = %v, want %v", c.cred, c.want, got, c.ok)
		}
	}
}

func TestResolveAuthProvider_StoreOAuth(t *testing.T) {
	withTempStore(t)
	cred := &AuthProfileCredential{
		CredType: CredentialOAuth,
		Provider: ProviderAnthropic,
		Access:   "access-token",
		Refresh:  "refresh-token",
		Expires:  9_999_999_999_999, // far future
	}
	if err := UpsertProfile("anthropic:test", cred, zap.NewNop()); err != nil {
		t.Fatalf("UpsertProfile: %v", err)
	}

	tp, err := ResolveAuthProvider(context.Background(), ProviderAnthropic, zap.NewNop())
	if err != nil {
		t.Fatalf("ResolveAuthProvider: %v", err)
	}
	defer tp.Close()

	if tp.Mode() != AuthModeOAuth {
		t.Errorf("mode = %q, want oauth", tp.Mode())
	}
	if tp.Source() != "auth-store" {
		t.Errorf("source = %q, want auth-store", tp.Source())
	}
	if tp.ProfileID() != "anthropic:test" {
		t.Errorf("profileID = %q, want anthropic:test", tp.ProfileID())
	}
	tok, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "access-token" {
		t.Errorf("token = %q, want access-token", tok)
	}
}

func TestResolveAuthProvider_StoreAPIKey(t *testing.T) {
	withTempStore(t)
	cred := &AuthProfileCredential{
		CredType: CredentialAPIKey,
		Provider: ProviderOpenAI,
		Key:      "sk-test",
		Email:    "u@example.com",
	}
	if err := UpsertProfile("openai:test", cred, zap.NewNop()); err != nil {
		t.Fatalf("UpsertProfile: %v", err)
	}

	tp, err := ResolveAuthProvider(context.Background(), ProviderOpenAI, zap.NewNop())
	if err != nil {
		t.Fatalf("ResolveAuthProvider: %v", err)
	}
	defer tp.Close()
	if tp.Mode() != AuthModeAPIKey {
		t.Errorf("mode = %q, want api-key", tp.Mode())
	}
	if tp.Email() != "u@example.com" {
		t.Errorf("email = %q, want u@example.com", tp.Email())
	}
	tok, _ := tp.Token(context.Background())
	if tok != "sk-test" {
		t.Errorf("token = %q, want sk-test", tok)
	}
}

func TestResolveAuthProvider_StoreToken(t *testing.T) {
	withTempStore(t)
	cred := &AuthProfileCredential{
		CredType: CredentialToken,
		Provider: ProviderGitHubCopilot,
		Token:    "gho_test",
	}
	if err := UpsertProfile("copilot:test", cred, zap.NewNop()); err != nil {
		t.Fatalf("UpsertProfile: %v", err)
	}

	tp, err := ResolveAuthProvider(context.Background(), ProviderGitHubCopilot, zap.NewNop())
	if err != nil {
		t.Fatalf("ResolveAuthProvider: %v", err)
	}
	defer tp.Close()
	if tp.Mode() != AuthModeToken {
		t.Errorf("mode = %q, want token", tp.Mode())
	}
}

func TestResolveAuthProvider_SkipsEmptyKeys(t *testing.T) {
	withTempStore(t)
	cred := &AuthProfileCredential{
		CredType: CredentialAPIKey,
		Provider: ProviderOpenAI,
		Key:      "   ", // whitespace counts as empty after TrimSpace
	}
	if err := UpsertProfile("openai:empty", cred, zap.NewNop()); err != nil {
		t.Fatalf("UpsertProfile: %v", err)
	}
	if _, err := ResolveAuthProvider(context.Background(), ProviderOpenAI, zap.NewNop()); err == nil {
		t.Fatal("expected error when store has empty key and no env fallback")
	}
}

func TestResolveAuthProvider_OpenAICodexFallsBackToOpenAI(t *testing.T) {
	withTempStore(t)
	cred := &AuthProfileCredential{
		CredType: CredentialOAuth,
		Provider: ProviderOpenAICodex,
		Access:   "codex-token",
		Refresh:  "rt",
		Expires:  9_999_999_999_999,
	}
	if err := UpsertProfile(CodexCliProfileID, cred, zap.NewNop()); err != nil {
		t.Fatalf("UpsertProfile: %v", err)
	}
	tp, err := ResolveAuthProvider(context.Background(), ProviderOpenAI, zap.NewNop())
	if err != nil {
		t.Fatalf("ResolveAuthProvider for OpenAI should pick up codex profile: %v", err)
	}
	defer tp.Close()
	if tp.Mode() != AuthModeOAuth {
		t.Errorf("mode = %q, want oauth", tp.Mode())
	}
}

func TestEnvFallbackProvider_AllProviders(t *testing.T) {
	cases := []struct {
		provider  ProviderID
		envVar    string
		envValue  string
		wantMode  AuthMode
		wantToken string
		wantSrc   string
	}{
		{ProviderAnthropic, "ANTHROPIC_OAUTH_TOKEN", "ant-oauth", AuthModeOAuth, "ant-oauth", "env:ANTHROPIC_OAUTH_TOKEN"},
		{ProviderAnthropic, "ANTHROPIC_API_KEY", "ant-key", AuthModeAPIKey, "ant-key", "env:ANTHROPIC_API_KEY"},
		{ProviderOpenAI, "OPENAI_API_KEY", "oai-key", AuthModeAPIKey, "oai-key", "env:OPENAI_API_KEY"},
		{ProviderGitHubCopilot, "GITHUB_COPILOT_TOKEN", "gho", AuthModeToken, "gho", "env:GITHUB_COPILOT_TOKEN"},
		{ProviderGitHubModels, "GITHUB_TOKEN", "ghp", AuthModeToken, "ghp", "env:GITHUB_TOKEN"},
	}
	for _, c := range cases {
		t.Run(c.envVar, func(t *testing.T) {
			t.Setenv(c.envVar, c.envValue)
			tp := envFallbackProvider(c.provider)
			if tp == nil {
				t.Fatalf("envFallbackProvider(%s) returned nil", c.provider)
			}
			if tp.Mode() != c.wantMode {
				t.Errorf("mode = %q, want %q", tp.Mode(), c.wantMode)
			}
			if tp.Source() != c.wantSrc {
				t.Errorf("source = %q, want %q", tp.Source(), c.wantSrc)
			}
			tok, err := tp.Token(context.Background())
			if err != nil || tok != c.wantToken {
				t.Errorf("token = %q err=%v, want %q nil", tok, err, c.wantToken)
			}
		})
	}
}

func TestEnvFallbackProvider_NoMatchReturnsNil(t *testing.T) {
	// Clear all known env vars for the providers we test.
	for _, k := range []string{
		"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY",
		"OPENAI_API_KEY",
		"GITHUB_COPILOT_TOKEN",
		"GITHUB_TOKEN", "GH_TOKEN", "GITHUB_MODELS_TOKEN",
	} {
		t.Setenv(k, "")
	}
	for _, p := range []ProviderID{ProviderAnthropic, ProviderOpenAI, ProviderGitHubCopilot, ProviderGitHubModels} {
		if tp := envFallbackProvider(p); tp != nil {
			t.Errorf("envFallbackProvider(%s) = non-nil with all envs cleared", p)
		}
	}
}

func TestResolveAuth_LegacyPrefixedString(t *testing.T) {
	withTempStore(t)
	cred := &AuthProfileCredential{
		CredType: CredentialAPIKey,
		Provider: ProviderOpenAI,
		Key:      "sk-legacy",
	}
	if err := UpsertProfile("openai:legacy", cred, zap.NewNop()); err != nil {
		t.Fatalf("UpsertProfile: %v", err)
	}

	resolved, err := ResolveAuth(context.Background(), ProviderOpenAI, zap.NewNop())
	if err != nil {
		t.Fatalf("ResolveAuth: %v", err)
	}
	if !strings.HasPrefix(resolved.APIKey, "apikey:") {
		t.Errorf("APIKey = %q, want apikey: prefix", resolved.APIKey)
	}
	if resolved.Mode != AuthModeAPIKey {
		t.Errorf("Mode = %q, want api-key", resolved.Mode)
	}
	if resolved.ProfileID != "openai:legacy" {
		t.Errorf("ProfileID = %q, want openai:legacy", resolved.ProfileID)
	}
}

func TestResolveAuth_NoCredentialsErrors(t *testing.T) {
	withTempStore(t)
	for _, k := range []string{
		"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY",
		"OPENAI_API_KEY",
	} {
		t.Setenv(k, "")
	}
	if _, err := ResolveAuth(context.Background(), ProviderAnthropic, zap.NewNop()); err == nil {
		t.Fatal("expected error when store empty and env empty")
	}
}

func TestFormatAuthStatus_AllNotConnected(t *testing.T) {
	withTempStore(t)
	for _, k := range []string{
		"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY",
		"OPENAI_API_KEY",
		"GITHUB_COPILOT_TOKEN",
		"GITHUB_TOKEN", "GH_TOKEN", "GITHUB_MODELS_TOKEN",
	} {
		t.Setenv(k, "")
	}
	out := FormatAuthStatus(zap.NewNop())
	if !strings.Contains(out, "not connected") {
		t.Errorf("expected 'not connected' in output, got: %s", out)
	}
}

func TestFormatAuthStatus_StoreProfile(t *testing.T) {
	withTempStore(t)
	cred := &AuthProfileCredential{
		CredType: CredentialOAuth,
		Provider: ProviderAnthropic,
		Access:   "tok",
		Expires:  9_999_999_999_999,
	}
	if err := UpsertProfile("anthropic:default", cred, zap.NewNop()); err != nil {
		t.Fatalf("UpsertProfile: %v", err)
	}
	out := FormatAuthStatus(zap.NewNop())
	if !strings.Contains(out, "type=oauth") {
		t.Errorf("expected 'type=oauth' in output, got: %s", out)
	}
}

func TestFormatAuthStatus_NonDefaultProfileShows(t *testing.T) {
	// The legacy hardcoded lookup missed profiles whose ID was not exactly
	// "<provider>:default" — e.g. synced ones. The new pickProfile must surface
	// any matching profile.
	withTempStore(t)
	for _, k := range []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		t.Setenv(k, "")
	}
	cred := &AuthProfileCredential{
		CredType: CredentialOAuth,
		Provider: ProviderAnthropic,
		Access:   "tok",
		Expires:  9_999_999_999_999,
	}
	if err := UpsertProfile("anthropic-oauth-sync", cred, zap.NewNop()); err != nil {
		t.Fatalf("UpsertProfile: %v", err)
	}
	out := FormatAuthStatus(zap.NewNop())
	if strings.Contains(out, "Anthropic: not connected") {
		t.Errorf("non-default profile should be reflected, got: %s", out)
	}
	if !strings.Contains(out, "type=oauth") {
		t.Errorf("expected 'type=oauth' for synced profile, got: %s", out)
	}
}

func TestFormatAuthStatus_EnvFallback(t *testing.T) {
	withTempStore(t)
	// Only env set, no store profile. Status should show env fallback.
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")
	out := FormatAuthStatus(zap.NewNop())
	if !strings.Contains(out, "env fallback") && !strings.Contains(out, "env:ANTHROPIC_API_KEY") {
		t.Errorf("expected env-fallback indicator, got: %s", out)
	}
}

func TestLogout_RemovesAllProfilesForProvider(t *testing.T) {
	withTempStore(t)
	// Seed two Anthropic profiles with different IDs.
	for _, id := range []string{"anthropic:default", "anthropic-oauth-sync"} {
		cred := &AuthProfileCredential{
			CredType: CredentialOAuth,
			Provider: ProviderAnthropic,
			Access:   "x",
		}
		if err := UpsertProfile(id, cred, zap.NewNop()); err != nil {
			t.Fatalf("UpsertProfile(%s): %v", id, err)
		}
	}
	if err := Logout(ProviderAnthropic, zap.NewNop()); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	store := LoadStore(zap.NewNop())
	for id, c := range store.Profiles {
		if c != nil && c.Provider == ProviderAnthropic {
			t.Errorf("Logout left Anthropic profile %s behind", id)
		}
	}
}
