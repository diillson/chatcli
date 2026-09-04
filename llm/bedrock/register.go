/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/registry"
)

func init() {
	registry.Register(registry.ProviderInfo{
		Name:         "BEDROCK",
		DisplayName:  "AWS Bedrock (all text models available to your account)",
		RequiresAuth: false, // Auth comes from AWS credential chain, not a string APIKey
		EnvKeys:      []string{"BEDROCK_REGION", "AWS_REGION", "BEDROCK_PROFILE", "AWS_PROFILE"},
		Factory: func(cfg registry.ProviderConfig) (client.LLMClient, error) {
			model := cfg.Model
			if model == "" {
				model = config.DefaultBedrockModel
			}
			// Same resolution the manager and every other Bedrock surface
			// use (BEDROCK_* first, then AWS_*, then the loaded .env) — this
			// factory used to read AWS_PROFILE from the process only, so a
			// BEDROCK_PROFILE or an .env-only profile selected a different
			// account here than in chat.
			envRegion, _ := ResolveRegion()
			region := firstNonEmpty(
				cfg.ExtraConfig["region"],
				envRegion,
				config.DefaultBedrockRegion,
			)
			envProfile, _ := ResolveProfile()
			profile := firstNonEmpty(
				cfg.ExtraConfig["profile"],
				envProfile,
			)
			return NewBedrockClient(model, region, profile, cfg.Logger, cfg.MaxRetries, cfg.Backoff), nil
		},
	})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
