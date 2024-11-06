package utils

import (
	"fmt"
	"os/exec"
	"strings"
)

// GetGitInfo retorna informações sobre o repositório Git atual, incluindo status, log recente e branches.
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
		return "", fmt.Errorf("erro ao obter o status do Git: %w", err)
	}
	gitData.WriteString("Status:\n")
	gitData.WriteString(string(statusOut))

	// Obter log recente
	logCmd := exec.Command("git", "log", "--oneline", "-5")
	logOut, err := logCmd.Output()
	if err != nil {
		return "", fmt.Errorf("erro ao obter o log do Git: %w", err)
	}
	gitData.WriteString("\nLog Recente:\n")
	gitData.WriteString(string(logOut))

	// Obter branches
	branchCmd := exec.Command("git", "branch")
	branchOut, err := branchCmd.Output()
	if err != nil {
		return "", fmt.Errorf("erro ao obter as branches do Git: %w", err)
	}
	gitData.WriteString("\nBranches:\n")
	gitData.WriteString(string(branchOut))

	return gitData.String(), nil
}
