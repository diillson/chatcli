//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

// forceRefreshPrompt é um stub para Windows, onde SIGWINCH não se aplica.
// A função não faz nada, pois não há um equivalente direto.
func (cli *ChatCLI) forceRefreshPrompt() {
	// No-op for Windows
}
