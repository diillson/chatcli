/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import pb "github.com/diillson/chatcli/proto/chatcli/v1"

// usageTokens returns the prompt and completion tokens the server reported,
// or the caller's character-based fallback when the response carried no
// usage (a server older than the usage fields).
func usageTokens(u *pb.TokenUsage, fallbackIn, fallbackOut int64) (int64, int64) {
	if u == nil {
		return fallbackIn, fallbackOut
	}
	return int64(u.GetPromptTokens()), int64(u.GetCompletionTokens())
}

// servedProviderModel attributes a call to the provider and model that
// answered, falling back to what was requested when the server did not say.
func servedProviderModel(servedProvider, servedModel, requestedProvider, requestedModel string) (string, string) {
	provider, model := servedProvider, servedModel
	if provider == "" {
		provider = requestedProvider
	}
	if model == "" {
		model = requestedModel
	}
	return provider, model
}
