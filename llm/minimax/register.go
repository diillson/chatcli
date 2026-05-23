package minimax

import (
	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/registry"
)

func init() {
	registry.Register(registry.ProviderInfo{
		Name:         "MINIMAX",
		DisplayName:  "MiniMax",
		RequiresAuth: true,
		EnvKeys:      []string{"MINIMAX_API_KEY"},
		Factory: func(cfg registry.ProviderConfig) (client.LLMClient, error) {
			model := cfg.Model
			if model == "" {
				model = config.DefaultMiniMaxModel
			}
			provider := auth.NewStaticTokenProvider(cfg.APIKey, auth.AuthModeAPIKey, "")
			return NewMiniMaxClient(provider, model, cfg.Logger, cfg.MaxRetries, cfg.Backoff), nil
		},
	})
}
