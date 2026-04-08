/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openrouter

import (
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/registry"
)

func init() {
	registry.Register(registry.ProviderInfo{
		Name:         "OPENROUTER",
		DisplayName:  "OpenRouter",
		RequiresAuth: true,
		EnvKeys:      []string{"OPENROUTER_API_KEY"},
		Factory: func(cfg registry.ProviderConfig) (client.LLMClient, error) {
			model := cfg.Model
			if model == "" {
				model = config.DefaultOpenRouterModel
			}
			return NewOpenRouterClient(cfg.APIKey, model, cfg.Logger, cfg.MaxRetries, cfg.Backoff), nil
		},
	})
}
