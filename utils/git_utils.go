package utils

import (
	"fmt"
	"os/exec"
	"strings"
)

// GetGitInfo retorna informações detalhadas sobre o repositório Git atual
func GetGitInfo() (string, error) {
	// Verificar se estamos dentro de um repositório Git
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("não é um repositório Git")
	}

	var gitData strings.Builder

	// Obter informações do repositório remoto
	remoteCmd := exec.Command("git", "remote", "-v")
	remoteOut, err := remoteCmd.Output()
	if err == nil {
		gitData.WriteString("Repositórios Remotos:\n")
		gitData.WriteString(string(remoteOut))
		gitData.WriteString("\n")
	}

	// Obter branch atual
	currentBranchCmd := exec.Command("git", "branch", "--show-current")
	currentBranchOut, err := currentBranchCmd.Output()
	if err == nil {
		gitData.WriteString("Branch Atual: ")
		gitData.WriteString(string(currentBranchOut))
		gitData.WriteString("\n")
	}

	// Obter status detalhado
	statusCmd := exec.Command("git", "status", "-s", "-b")
	statusOut, err := statusCmd.Output()
	if err == nil {
		gitData.WriteString("\nStatus Resumido:\n")
		gitData.WriteString(string(statusOut))
		gitData.WriteString("\n")
	}

	// Obter arquivos modificados com diferenças
	diffCmd := exec.Command("git", "diff", "--stat")
	diffOut, err := diffCmd.Output()
	if err == nil {
		gitData.WriteString("\nArquivos Modificados (estatísticas):\n")
		gitData.WriteString(string(diffOut))
	}

	// Obter diferenças detalhadas
	diffDetailedCmd := exec.Command("git", "diff")
	diffDetailedOut, err := diffDetailedCmd.Output()
	if err == nil {
		gitData.WriteString("\nDiferenças Detalhadas:\n")
		gitData.WriteString(string(diffDetailedOut))
	}

	// Obter arquivos não rastreados
	untrackedCmd := exec.Command("git", "ls-files", "--others", "--exclude-standard")
	untrackedOut, err := untrackedCmd.Output()
	if err == nil {
		gitData.WriteString("\nArquivos Não Rastreados:\n")
		gitData.WriteString(string(untrackedOut))
	}

	// Obter log mais detalhado
	logCmd := exec.Command("git", "log", "-5", "--pretty=format:%h - %an, %ar : %s")
	logOut, err := logCmd.Output()
	if err == nil {
		gitData.WriteString("\nÚltimos 5 Commits:\n")
		gitData.WriteString(string(logOut))
		gitData.WriteString("\n")
	}

	// Obter estatísticas do repositório
	contributorsCmd := exec.Command("git", "shortlog", "-sn", "--all")
	contributorsOut, err := contributorsCmd.Output()
	if err == nil {
		gitData.WriteString("\nContribuições por Autor:\n")
		gitData.WriteString(string(contributorsOut))
	}

	// Obter tags
	tagsCmd := exec.Command("git", "tag", "-n")
	tagsOut, err := tagsCmd.Output()
	if err == nil {
		gitData.WriteString("\nTags:\n")
		gitData.WriteString(string(tagsOut))
	}

	// Obter configurações do repositório
	configCmd := exec.Command("git", "config", "--local", "--list")
	configOut, err := configCmd.Output()
	if err == nil {
		gitData.WriteString("\nConfigurações Locais do Repositório:\n")
		gitData.WriteString(string(configOut))
	}

	return gitData.String(), nil
}

// Funções abaixo serão implementadas em nova Feature planejada. :D

// Função auxiliar para obter diferenças específicas de um arquivo
func GetFileDiff(filepath string) (string, error) {
	cmd := exec.Command("git", "diff", filepath)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("erro ao obter diferenças do arquivo %s: %w", filepath, err)
	}
	return string(output), nil
}

// Função auxiliar para obter o histórico de um arquivo específico
func GetFileHistory(filepath string) (string, error) {
	cmd := exec.Command("git", "log", "--follow", "--pretty=format:%h - %an, %ar : %s", "--", filepath)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("erro ao obter histórico do arquivo %s: %w", filepath, err)
	}
	return string(output), nil
}

// Função auxiliar para obter blame de um arquivo
func GetFileBlame(filepath string) (string, error) {
	cmd := exec.Command("git", "blame", filepath)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("erro ao obter blame do arquivo %s: %w", filepath, err)
	}
	return string(output), nil
}
