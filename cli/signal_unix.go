//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"os"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

// forceRefreshPrompt envia um sinal SIGWINCH para o processo atual no Unix.
func (cli *ChatCLI) forceRefreshPrompt() {
	//resetTerminal(cli.logger)

	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		cli.logger.Warn("Não foi possível encontrar o processo para forçar o refresh", zap.Error(err))
		return
	}
	if err := p.Signal(unix.SIGWINCH); err != nil {
		cli.logger.Warn("Não foi possível enviar o sinal SIGWINCH para forçar o refresh", zap.Error(err))
	}
}
