package googleai

import (
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/registry"
)

func init() {
	registry.Register(registry.ProviderInfo{
		Name:         "GOOGLEAI",
		DisplayName:  "Google Gemini",
		RequiresAuth: true,
		EnvKeys:      []string{"GOOGLEAI_API_KEY", "GOOGLE_API_KEY"},
		Factory: func(cfg registry.ProviderConfig) (client.LLMClient, error) {
			model := cfg.Model
			if model == "" {
				model = config.DefaultGoogleAIModel
			}
			return NewGeminiClient(cfg.APIKey, model, cfg.Logger, cfg.MaxRetries, cfg.Backoff), nil
		},
	})
}
