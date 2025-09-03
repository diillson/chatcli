/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package utils

import (
	"fmt"
	"os/exec"
	"strings"
)

// GetGitInfo agora aceita um CommandExecutor.
func GetGitInfo(executor CommandExecutor) (string, error) {
	// Verificar se estamos dentro de um repositório Git
	if err := executor.Run("git", "rev-parse", "--is-inside-work-tree"); err != nil {
		return "", fmt.Errorf("não é um repositório Git")
	}

	var gitData strings.Builder

	// Obter informações do repositório remoto
	remoteOut, err := executor.Output("git", "remote", "-v")
	if err == nil {
		gitData.WriteString("Repositórios Remotos:\n")
		gitData.WriteString(string(remoteOut))
		gitData.WriteString("\n")
	}

	// Obter branch atual
	currentBranchOut, err := executor.Output("git", "branch", "--show-current")
	if err == nil {
		gitData.WriteString("Branch Atual: ")
		gitData.WriteString(string(currentBranchOut))
		gitData.WriteString("\n")
	}

	// Obter status detalhado
	statusOut, err := executor.Output("git", "status", "-s", "-b")
	if err == nil {
		gitData.WriteString("\nStatus Resumido:\n")
		gitData.WriteString(string(statusOut))
		gitData.WriteString("\n")
	}

	// Obter arquivos modificados com diferenças
	diffOut, err := executor.Output("git", "diff", "--stat")
	if err == nil {
		gitData.WriteString("\nArquivos Modificados (estatísticas):\n")
		gitData.WriteString(string(diffOut))
	}

	// Obter diferenças detalhadas
	diffDetailedOut, err := executor.Output("git", "diff")
	if err == nil {
		gitData.WriteString("\nDiferenças Detalhadas:\n")
		gitData.WriteString(string(diffDetailedOut))
	}

	// Obter arquivos não rastreados
	untrackedOut, err := executor.Output("git", "ls-files", "--others", "--exclude-standard")
	if err == nil {
		gitData.WriteString("\nArquivos Não Rastreados:\n")
		gitData.WriteString(string(untrackedOut))
	}

	// Obter log mais detalhado
	logOut, err := executor.Output("git", "log", "-5", "--pretty=format:%h - %an, %ar : %s")
	if err == nil {
		gitData.WriteString("\nÚltimos 5 Commits:\n")
		gitData.WriteString(string(logOut))
		gitData.WriteString("\n")
	}

	// Obter estatísticas do repositório
	contributorsOut, err := executor.Output("git", "shortlog", "-sn", "--all")
	if err == nil {
		gitData.WriteString("\nContribuições por Autor:\n")
		gitData.WriteString(string(contributorsOut))
	}

	// Obter tags
	tagsOut, err := executor.Output("git", "tag", "-n")
	if err == nil {
		gitData.WriteString("\nTags:\n")
		gitData.WriteString(string(tagsOut))
	}

	// Obter configurações do repositório
	configOut, err := executor.Output("git", "config", "--local", "--list")
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
