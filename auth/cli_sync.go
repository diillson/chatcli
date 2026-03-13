package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
)

// SyncExternalCliCreds discovers credentials from external CLI tools
// (Claude Code, OpenAI Codex CLI) and imports them into ChatCLI's auth store.
// Returns (true, nil) if any credentials were synced, (false, nil) if none found.
func SyncExternalCliCreds(logger *zap.Logger) (bool, error) {
	synced := false

	// Try Claude Code credentials
	if imported := syncClaudeCodeCreds(logger); imported {
		synced = true
	}

	// Try Codex CLI credentials
	if imported := syncCodexCliCreds(logger); imported {
		synced = true
	}

	return synced, nil
}

// syncClaudeCodeCreds syncs credentials from Claude Code (~/.claude/).
func syncClaudeCodeCreds(logger *zap.Logger) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	// Try multiple known locations
	paths := []string{
		filepath.Join(home, ".claude", "credentials.json"),
		filepath.Join(home, ".claude.json"),
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		cred := parseClaudeCredentials(data, logger)
		if cred == nil {
			continue
		}

		// Check if we already have fresher credentials
		store := LoadStore(logger)
		existing := store.Profiles["anthropic-oauth-sync"]
		if existing != nil && existing.Expires > cred.Expires {
			logger.Debug("existing Anthropic credentials are newer, skipping sync")
			return false
		}

		if err := UpsertProfile("anthropic-oauth-sync", cred, logger); err != nil {
			logger.Warn("failed to save synced Anthropic credentials", zap.Error(err))
			return false
		}

		logger.Info("synced Anthropic credentials from Claude Code",
			zap.String("source", path))
		return true
	}

	return false
}

// syncCodexCliCreds syncs credentials from OpenAI Codex CLI (~/.codex/).
func syncCodexCliCreds(logger *zap.Logger) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	paths := []string{
		filepath.Join(home, ".codex", "credentials.json"),
		filepath.Join(home, ".codex", "auth.json"),
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		cred := parseCodexCredentials(data, logger)
		if cred == nil {
			continue
		}

		store := LoadStore(logger)
		existing := store.Profiles["openai-codex-sync"]
		if existing != nil && existing.Expires > cred.Expires {
			logger.Debug("existing OpenAI Codex credentials are newer, skipping sync")
			return false
		}

		if err := UpsertProfile("openai-codex-sync", cred, logger); err != nil {
			logger.Warn("failed to save synced Codex credentials", zap.Error(err))
			return false
		}

		logger.Info("synced OpenAI credentials from Codex CLI",
			zap.String("source", path))
		return true
	}

	return false
}

// parseClaudeCredentials parses Claude Code credential formats.
func parseClaudeCredentials(data []byte, logger *zap.Logger) *AuthProfileCredential {
	// Format 1: { "claudeAiOauth": { "accessToken": "...", "refreshToken": "...", "expiresAt": "..." } }
	var format1 struct {
		ClaudeAiOauth struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    string `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &format1); err == nil && format1.ClaudeAiOauth.AccessToken != "" {
		var expires int64
		if t, err := time.Parse(time.RFC3339, format1.ClaudeAiOauth.ExpiresAt); err == nil {
			expires = t.Unix()
		}
		if expires > 0 && time.Now().Unix() > expires {
			logger.Debug("Claude Code token expired, skipping")
			return nil
		}
		return &AuthProfileCredential{
			CredType: CredentialOAuth,
			Provider: ProviderAnthropic,
			Access:   format1.ClaudeAiOauth.AccessToken,
			Refresh:  format1.ClaudeAiOauth.RefreshToken,
			Expires:  expires,
		}
	}

	// Format 2: { "access_token": "...", "refresh_token": "...", "expires_at": 123456 }
	var format2 struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresAt    int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &format2); err == nil && format2.AccessToken != "" {
		if format2.ExpiresAt > 0 && time.Now().Unix() > format2.ExpiresAt {
			logger.Debug("Claude credential expired, skipping")
			return nil
		}
		return &AuthProfileCredential{
			CredType: CredentialOAuth,
			Provider: ProviderAnthropic,
			Access:   format2.AccessToken,
			Refresh:  format2.RefreshToken,
			Expires:  format2.ExpiresAt,
		}
	}

	return nil
}

// parseCodexCredentials parses Codex CLI credential formats.
func parseCodexCredentials(data []byte, logger *zap.Logger) *AuthProfileCredential {
	var creds struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresAt    int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &creds); err == nil && creds.AccessToken != "" {
		if creds.ExpiresAt > 0 && time.Now().Unix() > creds.ExpiresAt {
			logger.Debug("Codex CLI token expired, skipping")
			return nil
		}
		return &AuthProfileCredential{
			CredType: CredentialOAuth,
			Provider: ProviderOpenAICodex,
			Access:   creds.AccessToken,
			Refresh:  creds.RefreshToken,
			Expires:  creds.ExpiresAt,
		}
	}

	return nil
}
