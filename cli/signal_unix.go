//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

// forceRefreshPrompt envia um sinal SIGWINCH para o processo atual no Unix.
func (cli *ChatCLI) forceRefreshPrompt() {
	// Limpar buffer e resetar terminal antes do refresh
	fmt.Print("\r\033[K\033[0m") // Carriage return + Clear line + Reset colors
	os.Stdout.Sync()

	// Pequena pausa para o terminal processar
	time.Sleep(10 * time.Millisecond)

	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		cli.logger.Warn("Não foi possível encontrar o processo para forçar o refresh", zap.Error(err))
		return
	}
	if err := p.Signal(unix.SIGWINCH); err != nil {
		cli.logger.Warn("Não foi possível enviar o sinal SIGWINCH para forçar o refresh", zap.Error(err))
	}
}
