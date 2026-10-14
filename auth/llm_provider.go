/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import "github.com/diillson/chatcli/llm/catalog"

// ForLLMProvider maps an LLM provider name (an LLM_PROVIDER value) to the
// auth provider of its OAuth/local credential flow. The mapping lives on the
// provider's catalog descriptor; providers without such a flow report false.
func ForLLMProvider(llmProvider string) (ProviderID, bool) {
	d, ok := catalog.LookupProvider(llmProvider)
	if !ok || d.AuthProvider == "" {
		return "", false
	}
	return ProviderID(d.AuthProvider), true
}
