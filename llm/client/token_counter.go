/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"context"

	"github.com/diillson/chatcli/models"
)

// TokenCounter is the optional capability a client implements when its
// provider can count the tokens of a request without running it
// (Anthropic's count_tokens endpoint). The CLI uses it to calibrate its
// chars-per-token ratio exactly and to report precise prompt sizes; every
// caller must keep working when the capability is absent or the call
// fails, so no budget decision may depend on it.
type TokenCounter interface {
	LLMClient

	// CountTokens returns the input tokens the provider would bill for the
	// prompt and history as this client would send them.
	CountTokens(ctx context.Context, prompt string, history []models.Message) (int, error)
}

// AsTokenCounter returns the client as a TokenCounter when it is one.
func AsTokenCounter(c LLMClient) (TokenCounter, bool) {
	tc, ok := c.(TokenCounter)
	return tc, ok
}
