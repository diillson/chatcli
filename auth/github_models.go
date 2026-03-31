package auth

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
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

	// Reset terminal from raw mode (go-prompt/bubbletea) so stdin reads work
	if runtime.GOOS != "windows" {
		cmd := exec.Command("stty", "sane")
		cmd.Stdin = os.Stdin
		_ = cmd.Run()
	}

	// Prompt user for token
	fmt.Print("  Paste your GitHub token (ghp_... or github_pat_...): ")
	reader := bufio.NewReader(os.Stdin)
	rawToken, err := reader.ReadString('\n')
	if err != nil && rawToken == "" {
		return "", fmt.Errorf("failed to read token: %w", err)
	}
	token := strings.TrimSpace(rawToken)
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
