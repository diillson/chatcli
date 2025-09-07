/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/models"
)

type CommandHandler struct {
	cli *ChatCLI
}

func NewCommandHandler(cli *ChatCLI) *CommandHandler {
	return &CommandHandler{cli: cli}
}

// HandleCommand processa comandos do sistema
func (ch *CommandHandler) HandleCommand(userInput string) bool {
	userInput = strings.TrimSpace(userInput)

	// Sair
	switch userInput {
	case "/exit", "exit", "/quit", "quit":
		fmt.Println("Até mais!")
		return true
	}

	// Comandos que começam com "/"
	if strings.HasPrefix(userInput, "/") {
		// Comando sem argumentos (apenas a palavra após '/')
		cmd := strings.TrimPrefix(userInput, "/")

		switch cmd {
		case "reload":
			ch.cli.reloadConfiguration()
			return false
		case "help":
			ch.cli.showHelp()
			return false
		case "config", "status", "settings":
			ch.cli.showConfig()
			return false
		case "version", "v":
			ch.handleVersionCommand()
			return false
		case "nextchunk":
			return ch.cli.handleNextChunk()
		case "retry":
			return ch.cli.handleRetryLastChunk()
		case "retryall":
			return ch.cli.handleRetryAllChunks()
		case "skipchunk":
			return ch.cli.handleSkipChunk()
		case "newsession":
			ch.cli.history = []models.Message{}
			fmt.Println("Iniciada nova sessão de conversa; histórico foi limpo.")
			return false
		}

		// Comandos com argumentos
		if strings.HasPrefix(userInput, "/switch") {
			ch.cli.handleSwitchCommand(userInput)
			return false
		}
		if strings.HasPrefix(userInput, "/agent") || strings.HasPrefix(userInput, "/run") {
			ch.cli.handleAgentCommand(userInput)
			return false
		}

		// Desconhecido
		fmt.Printf("Comando desconhecido: '%s'. Use /help para ver os comandos disponíveis.\n", userInput)
		return false
	}

	// Não é comando
	return false

}
