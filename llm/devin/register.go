package devin

import (
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/registry"
)

func init() {
	registry.Register(registry.ProviderInfo{
		Name:         "DEVIN",
		DisplayName:  "Devin",
		RequiresAuth: true,
		EnvKeys:      []string{config.DevinAPIKeyEnv},
		Factory: func(cfg registry.ProviderConfig) (client.LLMClient, error) {
			model := cfg.Model
			if model == "" {
				model = config.DefaultDevinModel
			}
			apiCfg := ResolveAPIConfigFromEnv(cfg.Logger)
			apiCfg.APIKey = cfg.APIKey
			if cfg.ExtraConfig != nil {
				if v := cfg.ExtraConfig["org-id"]; v != "" {
					apiCfg.OrgID = v
				}
				if v := cfg.ExtraConfig["api-version"]; v != "" {
					apiCfg.Version = v
				}
				if v := cfg.ExtraConfig["base-url"]; v != "" {
					apiCfg.BaseURL = v
				}
			}
			api, err := NewAPI(apiCfg)
			if err != nil {
				return nil, err
			}
			return NewDevinClient(api, model, cfg.Logger), nil
		},
	})
}
