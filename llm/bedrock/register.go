/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"os"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/registry"
)

func init() {
	registry.Register(registry.ProviderInfo{
		Name:         "BEDROCK",
		DisplayName:  "AWS Bedrock (Anthropic Claude)",
		RequiresAuth: false, // Auth comes from AWS credential chain, not a string APIKey
		EnvKeys:      []string{"BEDROCK_REGION", "AWS_REGION", "AWS_PROFILE"},
		Factory: func(cfg registry.ProviderConfig) (client.LLMClient, error) {
			model := cfg.Model
			if model == "" {
				model = config.DefaultBedrockModel
			}
			region := firstNonEmpty(
				cfg.ExtraConfig["region"],
				os.Getenv("BEDROCK_REGION"),
				os.Getenv("AWS_REGION"),
				config.DefaultBedrockRegion,
			)
			profile := firstNonEmpty(
				cfg.ExtraConfig["profile"],
				os.Getenv("AWS_PROFILE"),
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
