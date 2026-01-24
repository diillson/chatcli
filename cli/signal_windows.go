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
	"os/exec"
	"runtime"

	"go.uber.org/zap"
)

// forceRefreshPrompt no Windows usa sequências de escape ANSI para limpar a linha.
func (cli *ChatCLI) forceRefreshPrompt() {
	// Limpar linha e resetar cores
	fmt.Print("\r\033[K\033[0m")
	os.Stdout.Sync()
}

func (cli *ChatCLI) restoreTerminal() {
	// Reset ANSI
	fmt.Print("\033[0m")
	os.Stdout.Sync()

	// Limpar tela no Windows
	cmd := exec.Command("cmd", "/c", "cls")
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		cli.logger.Warn("Falha ao limpar a tela no Windows", zap.Error(err))
	}
}

func resetTerminal(logger *zap.Logger) {
	if runtime.GOOS != "windows" {
		return
	}
	cmd := exec.Command("cmd", "/c", "cls")
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		logger.Warn("Falha ao limpar a tela no Windows", zap.Error(err))
	}
}
