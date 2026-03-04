package ollama

import (
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/registry"
)

func init() {
	registry.Register(registry.ProviderInfo{
		Name:         "OLLAMA",
		DisplayName:  "Ollama (Local)",
		RequiresAuth: false,
		EnvKeys:      []string{},
		Factory: func(cfg registry.ProviderConfig) (client.LLMClient, error) {
			model := cfg.Model
			if model == "" {
				model = config.DefaultOllamaModel
			}
			baseURL := "http://localhost:11434"
			if u, ok := cfg.ExtraConfig["base_url"]; ok && u != "" {
				baseURL = u
			}
			return NewClient(baseURL, model, cfg.Logger, cfg.MaxRetries, cfg.Backoff), nil
		},
	})
}
