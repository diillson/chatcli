package auth

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// LoginGitHubModelsPAT authenticates with GitHub Models marketplace using a
// GitHub Personal Access Token (PAT). The user can create a token at
// https://github.com/settings/tokens with no special scopes required for
// public model inference.
func LoginGitHubModelsPAT(_ context.Context, logger *zap.Logger) (string, error) {
	fmt.Println()
	fmt.Println(i18n.T("auth.github_models.header"))
	fmt.Println(i18n.T("auth.github_models.separator"))
	fmt.Println()
	fmt.Println(i18n.T("auth.github_models.description"))
	fmt.Println(i18n.T("auth.github_models.create_url"))
	fmt.Println(i18n.T("auth.github_models.scopes_hint"))
	fmt.Println()

	// Check if already available via env
	for _, envKey := range []string{"GITHUB_TOKEN", "GH_TOKEN", "GITHUB_MODELS_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
			fmt.Print(i18n.T("auth.github_models.found_env", envKey))
			profileID := "github-models:default"
			cred := &AuthProfileCredential{
				CredType: CredentialToken,
				Provider: ProviderGitHubModels,
				Token:    v,
				Expires:  0, // PAT tokens don't expire
			}
			if err := UpsertProfile(profileID, cred, logger); err != nil {
				return "", fmt.Errorf("%s: %w", i18n.T("auth.github_models.save_failed"), err)
			}
			logger.Info(i18n.T("auth.github_models.saved_from_env"), zap.String("env", envKey))
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
	fmt.Print(i18n.T("auth.github_models.prompt"))
	reader := bufio.NewReader(os.Stdin)
	rawToken, err := reader.ReadString('\n')
	if err != nil && rawToken == "" {
		return "", fmt.Errorf("%s: %w", i18n.T("auth.github_models.read_failed"), err)
	}
	token := strings.TrimSpace(rawToken)
	if token == "" {
		return "", fmt.Errorf("%s", i18n.T("auth.github_models.empty_token"))
	}

	profileID := "github-models:default"
	cred := &AuthProfileCredential{
		CredType: CredentialToken,
		Provider: ProviderGitHubModels,
		Token:    token,
		Expires:  0, // PAT tokens don't expire
	}
	if err := UpsertProfile(profileID, cred, logger); err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("auth.github_models.save_failed"), err)
	}

	logger.Info(i18n.T("auth.github_models.saved"), zap.String("profile", profileID))
	fmt.Println(i18n.T("auth.github_models.success"))
	return profileID, nil
}
