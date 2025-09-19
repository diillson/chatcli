//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"os"
	"os/exec"
	"runtime"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

// forceRefreshPrompt envia um sinal SIGWINCH para o processo atual no Unix.
func (cli *ChatCLI) forceRefreshPrompt() {
	//resetTerminal(cli.logger)

	// 2. Enviar sinal SIGWINCH apenas no Unix (como antes)
	if runtime.GOOS != "windows" {
		p, err := os.FindProcess(os.Getpid())
		if err != nil {
			cli.logger.Warn("Não foi possível encontrar o processo para forçar o refresh", zap.Error(err))
			return
		}
		if err := p.Signal(unix.SIGWINCH); err != nil {
			cli.logger.Warn("Não foi possível enviar o sinal SIGWINCH para forçar o refresh", zap.Error(err))
		}
	}
}

func resetTerminal(logger *zap.Logger) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "cls")
	} else {
		cmd = exec.Command("stty", "sane")
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		logger.Warn("Falha ao resetar terminal", zap.Error(err))
	}
}
