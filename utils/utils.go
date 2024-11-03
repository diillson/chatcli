package utils

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// ReadFileContent lê o conteúdo de um arquivo, expandindo ~ para o diretório home
func ReadFileContent(filePath string) (string, error) {
	// Expandir ~ para o diretório home
	expandedPath, err := ExpandPath(filePath)
	if err != nil {
		return "", err
	}

	// Tornar o caminho absoluto
	absPath, err := filepath.Abs(expandedPath)
	if err != nil {
		return "", fmt.Errorf("não foi possível determinar o caminho absoluto: %w", err)
	}

	// Verificar se o arquivo existe
	info, err := os.Stat(absPath)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("o arquivo não existe: %s", absPath)
	}
	if err != nil {
		return "", fmt.Errorf("erro ao acessar o arquivo: %w", err)
	}

	// Verificar se é um arquivo regular
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("o caminho não aponta para um arquivo regular: %s", absPath)
	}

	// Definir um limite de tamanho (por exemplo, 1MB)
	const maxSize = 1 * 1024 * 1024 // 1MB
	if info.Size() > maxSize {
		return "", fmt.Errorf("o arquivo é muito grande (limite de %d bytes)", maxSize)
	}

	// Ler o conteúdo do arquivo
	data, err := ioutil.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("erro ao ler o arquivo: %w", err)
	}

	content := string(data)

	// Opcional: Tratar diferentes codificações ou formatos, se necessário
	// Por exemplo, remover caracteres nulos:
	content = strings.ReplaceAll(content, "\x00", "")

	return content, nil
}

// IsTemporaryError verifica se o erro é temporário e pode ser retryado
func IsTemporaryError(err error) bool {
	if ne, ok := err.(net.Error); ok {
		return ne.Temporary() || ne.Timeout()
	}
	return false
}

func GetUserShell() string {
	shell := os.Getenv("SHELL")
	return filepath.Base(shell)
}

func getShellName(shellPath string) string {
	parts := strings.Split(shellPath, "/")
	return parts[len(parts)-1]
}

func GetShellHistoryFile() (string, error) {
	usr, err := user.Current()
	if err != nil {
		return "", err
	}

	shell := GetUserShell()
	var historyFile string

	switch shell {
	case "bash":
		historyFile = filepath.Join(usr.HomeDir, ".bash_history")
	case "zsh":
		historyFile = filepath.Join(usr.HomeDir, ".zsh_history")
	case "fish":
		historyFile = filepath.Join(usr.HomeDir, ".local", "share", "fish", "fish_history")
	default:
		return "", fmt.Errorf("shell não suportado ou não reconhecido: %s", shell)
	}

	return historyFile, nil
}

func GetShellHistory() (string, error) {
	historyFile, err := GetShellHistoryFile()
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(historyFile); os.IsNotExist(err) {
		return "", fmt.Errorf("arquivo de histórico não encontrado: %s", historyFile)
	}

	data, err := ioutil.ReadFile(historyFile)
	if err != nil {
		return "", err
	}

	shell := GetUserShell()
	if shell == "zsh" {
		processedData := processZshHistory(string(data))
		return processedData, nil
	}

	return string(data), nil
}

func processZshHistory(data string) string {
	lines := strings.Split(data, "\n")
	var commands []string

	for _, line := range lines {
		if strings.HasPrefix(line, ":") {
			idx := strings.Index(line, ";")
			if idx != -1 && idx+1 < len(line) {
				command := line[idx+1:]
				commands = append(commands, command)
			}
		} else {
			commands = append(commands, line)
		}
	}

	return strings.Join(commands, "\n")
}

func GetGitInfo() (string, error) {
	// Verificar se estamos dentro de um repositório Git
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("não é um repositório Git")
	}

	var gitData strings.Builder

	// Obter status
	statusCmd := exec.Command("git", "status")
	statusOut, err := statusCmd.Output()
	if err != nil {
		return "", err
	}
	gitData.WriteString("Status:\n")
	gitData.WriteString(string(statusOut))

	// Obter log recente
	logCmd := exec.Command("git", "log", "--oneline", "-5")
	logOut, err := logCmd.Output()
	if err != nil {
		return "", err
	}
	gitData.WriteString("\nLog Recente:\n")
	gitData.WriteString(string(logOut))

	// Obter branches
	branchCmd := exec.Command("git", "branch")
	branchOut, err := branchCmd.Output()
	if err != nil {
		return "", err
	}
	gitData.WriteString("\nBranches:\n")
	gitData.WriteString(string(branchOut))

	return gitData.String(), nil
}

func GetEnvVariables() string {
	envVars := os.Environ()
	return strings.Join(envVars, "\n")
}
