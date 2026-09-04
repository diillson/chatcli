/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"errors"
	"fmt"
	"testing"

	"github.com/diillson/chatcli/utils"
)

func TestIsContextOverflowError_ProviderBodies(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"openai chat", errors.New("This model's maximum context length is 128000 tokens. However, your messages resulted in 130000 tokens."), true},
		{"openai responses", errors.New("Your input exceeds the context window of this model. Please adjust your input and try again."), true},
		{"openai code", errors.New(`{"error":{"code":"context_length_exceeded","message":"..."}}`), true},
		{"anthropic", errors.New("prompt is too long: 250000 tokens > 200000 maximum"), true},
		{"gemini", errors.New("The input token count (1200000) exceeds the maximum number of tokens allowed (1048576)."), true},
		{"xai", errors.New("This request exceeds the maximum prompt length for the model."), true},
		{"mistral", errors.New("Prompt contains 40000 tokens, too large for model with 32768 maximum context length"), true},
		{"bedrock", errors.New("ValidationException: Input is too long for requested model."), true},
		{"groq", errors.New("Please reduce the length of the messages or completion."), true},
		{"status-aware 400", &utils.APIError{StatusCode: 400, Message: "invalid request: total tokens 300k exceed what the model supports"}, true},
		{"wrapped", fmt.Errorf("send: %w", &utils.APIError{StatusCode: 413, Message: "prompt length above the limit"}), true},
		{"rate limit", errors.New("Rate limit exceeded: too many requests, retry after 20s"), false},
		{"rate limit 429", &utils.APIError{StatusCode: 429, Message: "tokens per minute limit exceeded"}, false},
		{"auth", &utils.APIError{StatusCode: 401, Message: "invalid api key"}, false},
		{"timeout", errors.New("context deadline exceeded"), false},
		{"bad request other", &utils.APIError{StatusCode: 400, Message: "unknown parameter: foo"}, false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := IsContextOverflowError(c.err); got != c.want {
			t.Errorf("%s: got %v want %v (%v)", c.name, got, c.want, c.err)
		}
	}
}
