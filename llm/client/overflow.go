/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Context-overflow classification shared by every loop that can recover
 * from it (agent/coder, chat REPL, RPC chat, one-shot, MoA) and by the
 * fallback chain. Providers phrase the same condition differently; the
 * patterns below are the documented bodies of OpenAI (chat and
 * Responses), Anthropic, Gemini, xAI, Mistral, Groq, Cohere, DeepSeek,
 * Bedrock and OpenAI-compatible gateways.
 */
package client

import (
	"errors"
	"strings"

	"github.com/diillson/chatcli/utils"
)

// overflowPhrases are matched case-insensitively against the error text.
var overflowPhrases = []string{
	"context length",
	"context window",
	"context_length_exceeded",
	"context-length-exceeded",
	"maximum context",
	"max context",
	"prompt is too long",
	"prompt too long",
	"input too long",
	"input is too long",
	"request too large",
	"too many tokens",
	"token limit",
	"input token count",
	"maximum prompt length",
	"maximum number of tokens",
	"exceeds the maximum",
	"exceeds model",
	"reduce the length",
	"reduce your prompt",
	"messages too long",
	"conversation too long",
	"too long",
}

// IsContextOverflowError reports whether err is the provider telling us the
// request does not fit the model's context window (or its input token
// limit). A 400/413 utils.APIError whose body pairs a token/length word
// with an excess word counts too, so unlisted phrasings still classify.
func IsContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, p := range overflowPhrases {
		if strings.Contains(msg, p) {
			return true
		}
	}
	if strings.Contains(msg, "max_tokens") && strings.Contains(msg, "exceed") {
		return true
	}
	var apiErr *utils.APIError
	if errors.As(err, &apiErr) && (apiErr.StatusCode == 400 || apiErr.StatusCode == 413) {
		body := strings.ToLower(apiErr.Message)
		hasSubject := strings.Contains(body, "token") || strings.Contains(body, "context") || strings.Contains(body, "length") || strings.Contains(body, "prompt")
		hasExcess := strings.Contains(body, "exceed") || strings.Contains(body, "too large") || strings.Contains(body, "too long") || strings.Contains(body, "limit") || strings.Contains(body, "maximum")
		return hasSubject && hasExcess
	}
	return false
}
