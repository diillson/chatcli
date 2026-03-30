package auth

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
)

// LoginGitHubModelsPAT authenticates with GitHub Models marketplace using a
// GitHub Personal Access Token (PAT). The user can create a token at
// https://github.com/settings/tokens with no special scopes required for
// public model inference.
func LoginGitHubModelsPAT(_ context.Context, logger *zap.Logger) (string, error) {
	fmt.Println()
	fmt.Println("  GitHub Models — Authentication")
	fmt.Println("  ─────────────────────────────────")
	fmt.Println()
	fmt.Println("  GitHub Models uses a Personal Access Token (PAT) for authentication.")
	fmt.Println("  Create one at: https://github.com/settings/tokens")
	fmt.Println("  (No special scopes needed for model inference)")
	fmt.Println()

	// Check if already available via env
	for _, envKey := range []string{"GITHUB_TOKEN", "GH_TOKEN", "GITHUB_MODELS_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
			fmt.Printf("  Found token in %s, using it.\n", envKey)
			profileID := "github-models:default"
			cred := &AuthProfileCredential{
				CredType: CredentialToken,
				Provider: ProviderGitHubModels,
				Token:    v,
				Expires:  0, // PAT tokens don't expire
			}
			if err := UpsertProfile(profileID, cred, logger); err != nil {
				return "", fmt.Errorf("failed to save credential: %w", err)
			}
			logger.Info("GitHub Models token saved from env", zap.String("env", envKey))
			return profileID, nil
		}
	}

	// Prompt user for token
	fmt.Print("  Paste your GitHub token (ghp_... or github_pat_...): ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return "", fmt.Errorf("no input received")
	}
	token := strings.TrimSpace(scanner.Text())
	if token == "" {
		return "", fmt.Errorf("empty token")
	}

	profileID := "github-models:default"
	cred := &AuthProfileCredential{
		CredType: CredentialToken,
		Provider: ProviderGitHubModels,
		Token:    token,
		Expires:  0, // PAT tokens don't expire
	}
	if err := UpsertProfile(profileID, cred, logger); err != nil {
		return "", fmt.Errorf("failed to save credential: %w", err)
	}

	logger.Info("GitHub Models PAT saved", zap.String("profile", profileID))
	fmt.Println("  ✓ Token saved successfully!")
	return profileID, nil
}
