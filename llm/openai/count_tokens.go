/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * GPT token counting: OpenAI publishes no counting endpoint, so the count
 * runs locally with the model's BPE encoding (llm/tokenizer) — exact and
 * keyless, with the vocabulary cached once under ~/.chatcli/tokenizers.
 */
package openai

import (
	"context"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/tokenizer"
	"github.com/diillson/chatcli/models"
)

var _ client.TokenCounter = (*OpenAIClient)(nil)

// CountTokens implements client.TokenCounter.
func (c *OpenAIClient) CountTokens(_ context.Context, prompt string, history []models.Message) (int, error) {
	return tokenizer.CountChat(c.model, tokenizer.MessagesFromHistory(prompt, history))
}
