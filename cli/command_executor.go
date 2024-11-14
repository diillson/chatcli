package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/peterh/liner"
	"go.uber.org/zap"
)

// CommandExecutor executa comandos do sistema
type CommandExecutor struct{}

// ExecuteDirectCommand executa um comando diretamente no sistema
func (ce *CommandExecutor) ExecuteDirectCommand(command string, line *liner.State, logger *zap.Logger) {
	fmt.Println("Executando comando:", command)

	// Obter o shell do usuário
	userShell := os.Getenv("SHELL")
	shellPath, err := exec.LookPath(userShell)
	if err != nil {
		logger.Error("Erro ao localizar o shell", zap.Error(err))
		fmt.Println("Erro ao localizar o shell:", err)
		return
	}

	cmd := exec.Command(shellPath, "-c", command)

	// Capturar a saída do comando
	output, err := cmd.CombinedOutput()

	// Exibir a saída
	fmt.Println("Saída do comando:", string(output))

	if err != nil {
		fmt.Println("Erro ao executar comando:", err)
	}
}

// CompleteFilePath autocompleta caminhos de arquivos
func (ce *CommandExecutor) CompleteFilePath(prefix string) []string {
	var completions []string

	dir, filePrefix := filepath.Split(prefix)
	if dir == "" {
		dir = "."
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		return completions
	}

	for _, entry := range files {
		name := entry.Name()
		if strings.HasPrefix(name, filePrefix) {
			path := filepath.Join(dir, name)
			if entry.IsDir() {
				path += string(os.PathSeparator)
			}
			completions = append(completions, path)
		}
	}

	return completions
}
