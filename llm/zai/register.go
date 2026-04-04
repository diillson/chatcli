package zai

import (
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
			return NewZAIClient(cfg.APIKey, model, cfg.Logger, cfg.MaxRetries, cfg.Backoff), nil
		},
	})
}
