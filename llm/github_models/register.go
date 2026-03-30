package github_models

import (
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/registry"
)

func init() {
	registry.Register(registry.ProviderInfo{
		Name:         "GITHUB_MODELS",
		DisplayName:  "GitHub Models",
		RequiresAuth: true,
		EnvKeys:      []string{"GITHUB_TOKEN", "GH_TOKEN", "GITHUB_MODELS_TOKEN"},
		Factory: func(cfg registry.ProviderConfig) (client.LLMClient, error) {
			model := cfg.Model
			if model == "" {
				model = config.DefaultGitHubModelsModel
			}
			return NewGitHubModelsClient(cfg.APIKey, model, cfg.Logger, cfg.MaxRetries, cfg.Backoff), nil
		},
	})
}
