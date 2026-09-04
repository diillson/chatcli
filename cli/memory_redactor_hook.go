/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import "github.com/diillson/chatcli/cli/workspace/memory"

// The memory store masks secrets with the same redactor the LLM path uses,
// so /memory remember and /memory import can never persist a raw token.
func init() {
	memory.SetRedactor(redactSecretsForLLM)
}
