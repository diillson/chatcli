//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"fmt"
	"os"
)

// forceRefreshPrompt no Windows usa sequências de escape ANSI para limpar a linha.
func (cli *ChatCLI) forceRefreshPrompt() {
	// Limpar linha e resetar cores
	fmt.Print("\r\033[K\033[0m")
	os.Stdout.Sync()
}
