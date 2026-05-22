package claudeai

import (
	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/registry"
)

func init() {
	registry.Register(registry.ProviderInfo{
		Name:         "CLAUDEAI",
		DisplayName:  "Claude (Anthropic)",
		RequiresAuth: true,
		EnvKeys:      []string{"CLAUDEAI_API_KEY", "ANTHROPIC_API_KEY"},
		Factory: func(cfg registry.ProviderConfig) (client.LLMClient, error) {
			model := cfg.Model
			if model == "" {
				model = config.DefaultClaudeAIModel
			}
			provider := auth.NewStaticTokenProviderFromResolved(cfg.APIKey, auth.ProviderAnthropic)
			return NewClaudeClient(provider, model, cfg.Logger, cfg.MaxRetries, cfg.Backoff), nil
		},
	})
}
