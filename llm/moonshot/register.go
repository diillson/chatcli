package moonshot

import (
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/registry"
)

func init() {
	registry.Register(registry.ProviderInfo{
		Name:         "MOONSHOT",
		DisplayName:  "Moonshot (Kimi)",
		RequiresAuth: true,
		EnvKeys:      []string{"MOONSHOT_API_KEY"},
		Factory: func(cfg registry.ProviderConfig) (client.LLMClient, error) {
			model := cfg.Model
			if model == "" {
				model = config.DefaultMoonshotModel
			}
			return NewMoonshotClient(cfg.APIKey, model, cfg.Logger, cfg.MaxRetries, cfg.Backoff), nil
		},
	})
}
