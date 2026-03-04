package stackspotai

import (
	"fmt"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/registry"
	"github.com/diillson/chatcli/llm/token"
)

func init() {
	registry.Register(registry.ProviderInfo{
		Name:         "STACKSPOT",
		DisplayName:  "StackSpot AI",
		RequiresAuth: true,
		EnvKeys:      []string{"STACKSPOT_CLIENT_ID", "STACKSPOT_CLIENT_KEY"},
		Factory: func(cfg registry.ProviderConfig) (client.LLMClient, error) {
			clientID := cfg.ExtraConfig["client_id"]
			clientSecret := cfg.ExtraConfig["client_secret"]
			realm := cfg.ExtraConfig["realm"]
			agentID := cfg.ExtraConfig["agent_id"]

			if clientID == "" || clientSecret == "" {
				return nil, fmt.Errorf("StackSpot requires client_id and client_secret in ExtraConfig")
			}

			tm := token.NewTokenManager(clientID, clientSecret, realm, cfg.Logger)
			return NewStackSpotClient(tm, agentID, cfg.Logger, cfg.MaxRetries, cfg.Backoff), nil
		},
	})
}
