package copilot

import (
	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/registry"
)

func init() {
	registry.Register(registry.ProviderInfo{
		Name:         "COPILOT",
		DisplayName:  "GitHub Copilot",
		RequiresAuth: true,
		EnvKeys:      []string{"GITHUB_COPILOT_TOKEN"},
		Factory: func(cfg registry.ProviderConfig) (client.LLMClient, error) {
			model := cfg.Model
			if model == "" {
				model = config.DefaultCopilotModel
			}
			provider := auth.NewStaticTokenProviderFromResolved(cfg.APIKey, auth.ProviderGitHubCopilot)
			return NewClient(provider, model, cfg.Logger, cfg.MaxRetries, cfg.Backoff), nil
		},
	})
}
