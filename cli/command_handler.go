// command_handler.go
package cli

import (
	"fmt"
	"strings"
)

type CommandHandler struct {
	cli *ChatCLI
}

func NewCommandHandler(cli *ChatCLI) *CommandHandler {
	return &CommandHandler{cli: cli}
}

// Atualizar o método HandleCommand no CommandHandler para incluir os novos comandos
func (ch *CommandHandler) HandleCommand(userInput string) bool {
	switch {
	case userInput == "/exit" || userInput == "exit" || userInput == "/quit" || userInput == "quit":
		fmt.Println("Até mais!")
		return true
	case userInput == "/reload":
		ch.cli.reloadConfiguration()
		return false
	case strings.HasPrefix(userInput, "/agent") || strings.HasPrefix(userInput, "/run"):
		ch.cli.handleAgentCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/switch"):
		ch.cli.handleSwitchCommand(userInput)
		return false
	case userInput == "/help":
		ch.cli.showHelp()
		return false
	case userInput == "/nextchunk":
		return ch.cli.handleNextChunk()
	case userInput == "/retry":
		return ch.cli.handleRetryLastChunk()
	case userInput == "/retryall":
		return ch.cli.handleRetryAllChunks()
	case userInput == "/skipchunk":
		return ch.cli.handleSkipChunk()
	default:
		fmt.Println("Comando desconhecido. Use /help para ver os comandos disponíveis.")
		return false
	}
}
