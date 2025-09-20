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
// Isso força o go-prompt a redesenhar o prompt na próxima iteração do loop.
func (cli *ChatCLI) forceRefreshPrompt() {
	// No Windows, a melhor abordagem é limpar a linha atual e retornar o cursor.
	// O go-prompt irá então redesenhar o prefixo.
	// \r -> Carriage Return (volta ao início da linha)
	// \033[K -> Clear line from cursor to end
	fmt.Print("\r\033[K")
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
