package zai

import (
	"context"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/registry"
)

func init() {
	registry.Register(registry.ProviderInfo{
		Name:         "ZAI",
		DisplayName:  "ZAI (Zhipu AI)",
		RequiresAuth: true,
		EnvKeys:      []string{"ZAI_API_KEY"},
		Factory: func(cfg registry.ProviderConfig) (client.LLMClient, error) {
			model := cfg.Model
			if model == "" {
				model = config.DefaultZAIModel
			}
			provider := auth.NewStaticTokenProvider(cfg.APIKey, auth.AuthModeAPIKey, "")
			return NewZAIClient(context.Background(), provider, model, cfg.Logger, cfg.MaxRetries, cfg.Backoff), nil
		},
	})
}
