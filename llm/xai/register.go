package xai

import (
	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/registry"
)

func init() {
	registry.Register(registry.ProviderInfo{
		Name:         "XAI",
		DisplayName:  "xAI (Grok)",
		RequiresAuth: true,
		EnvKeys:      []string{"XAI_API_KEY"},
		Factory: func(cfg registry.ProviderConfig) (client.LLMClient, error) {
			model := cfg.Model
			if model == "" {
				model = config.DefaultXAIModel
			}
			provider := auth.NewStaticTokenProvider(cfg.APIKey, auth.AuthModeAPIKey, "")
			return NewXAIClient(provider, model, cfg.Logger, cfg.MaxRetries, cfg.Backoff), nil
		},
	})
}
