/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package utils

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// Definindo variáveis para as funções que queremos mockar
var (
	osGetenv    = os.Getenv
	userCurrent = user.Current
	osStat      = os.Stat
	osReadFile  = os.ReadFile
)

// GetUserShell retorna o shell do usuário atual.
func GetUserShell() string {
	shell := osGetenv("SHELL")
	return filepath.Base(shell)
}

// GetHomeDir retorna o diretório home do usuário atual.
func GetHomeDir() (string, error) {
	return os.UserHomeDir()
}

// GetShellConfigFilePath retorna o caminho do arquivo de configuração do shell com base no nome do shell.
func GetShellConfigFilePath(shellName string) string {
	homeDir, err := GetHomeDir()
	if err != nil {
		fmt.Println("Erro ao obter o diretório home do usuário:", err)
		return ""
	}

	switch shellName {
	case "zsh":
		return filepath.Join(homeDir, ".zshrc")
	case "bash":
		return filepath.Join(homeDir, ".bashrc")
	case "fish":
		return filepath.Join(homeDir, ".config", "fish", "config.fish")
	default:
		return ""
	}
}

// GetShellHistoryFile retorna o caminho do arquivo de histórico do shell.
func GetShellHistoryFile() (string, error) {
	usr, err := userCurrent()
	if err != nil {
		return "", fmt.Errorf("não foi possível obter o usuário atual: %w", err)
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

// GetShellHistory lê o arquivo de histórico do shell e retorna seu conteúdo.
func GetShellHistory() (string, error) {
	historyFile, err := GetShellHistoryFile()
	if err != nil {
		return "", err
	}

	if _, err := osStat(historyFile); os.IsNotExist(err) {
		return "", fmt.Errorf("arquivo de histórico não encontrado: %s", historyFile)
	}

	data, err := osReadFile(historyFile)
	if err != nil {
		return "", fmt.Errorf("erro ao ler o arquivo de histórico: %w", err)
	}

	shell := GetUserShell()
	if shell == "zsh" {
		processedData := processZshHistory(string(data))
		return processedData, nil
	}

	return string(data), nil
}

// processZshHistory processa o histórico do Zsh para remover metadados e retornar apenas os comandos.
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
