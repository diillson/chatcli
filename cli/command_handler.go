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

// HandleCommand processa os comandos da CLI
func (ch *CommandHandler) HandleCommand(userInput string) bool {
	switch {
	case userInput == "/exit" || userInput == "exit" || userInput == "/quit" || userInput == "quit":
		fmt.Println("Até mais!")
		return true
	case userInput == "/reload":
		ch.cli.reloadConfiguration()
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
	case strings.HasPrefix(userInput, "/rag"):
		ch.cli.handleRagCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/projeto"):
		ch.cli.handleProjectCommand(userInput)
		return false
	default:
		fmt.Println("Comando desconhecido. Use /help para ver os comandos disponíveis.")
		return false
	}
}
