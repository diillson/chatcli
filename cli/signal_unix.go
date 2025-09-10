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
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		cli.logger.Warn("Não foi possível encontrar o processo para forçar o refresh", zap.Error(err))
		return
	}
	// Usa unix.SIGWINCH, que está definido neste pacote
	if err := p.Signal(unix.SIGWINCH); err != nil {
		cli.logger.Warn("Não foi possível enviar o sinal SIGWINCH para forçar o refresh", zap.Error(err))
	}
}
