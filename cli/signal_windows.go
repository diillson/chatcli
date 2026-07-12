//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"fmt"
	"os"

	"go.uber.org/zap"
)

// forceRefreshPrompt limpa a linha atual e força o go-prompt a repintar o
// prompt. No Unix isso é feito com um SIGWINCH auto-enviado; o Windows não
// tem SIGWINCH, então o equivalente é injetar um key event no-op no buffer
// do console (WriteConsoleInputW) — o mesmo mecanismo do park auto-resume.
// O evento acorda o loop de leitura do go-prompt, que re-renderiza o prompt
// e re-avalia o live prefix (é isso que anima o spinner de prefixo e redesenha
// o prompt após um turno assíncrono terminar).
func (cli *ChatCLI) forceRefreshPrompt() {
	// Limpar linha e resetar cores
	fmt.Print("\r\033[K\033[0m")
	_ = os.Stdout.Sync()

	if err := injectPromptWake(); err != nil {
		// Sem console anexado (pipe/CI/daemon): o refresh vira só o
		// clear-line acima — mesma degradação do caminho Unix sem TTY.
		cli.logger.Debug("forceRefreshPrompt: wake injection unavailable", zap.Error(err))
	}
}
