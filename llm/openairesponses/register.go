package openairesponses

import (
	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/registry"
)

func init() {
	registry.Register(registry.ProviderInfo{
		Name:         "OPENAI_RESPONSES",
		DisplayName:  "OpenAI (Responses API)",
		RequiresAuth: true,
		EnvKeys:      []string{"OPENAI_API_KEY"},
		Factory: func(cfg registry.ProviderConfig) (client.LLMClient, error) {
			model := cfg.Model
			if model == "" {
				model = config.DefaultOpenAIModel
			}
			provider := auth.NewStaticTokenProviderFromResolved(cfg.APIKey, auth.ProviderOpenAI)
			return NewOpenAIResponsesClient(provider, model, cfg.Logger, cfg.MaxRetries, cfg.Backoff), nil
		},
	})
}
