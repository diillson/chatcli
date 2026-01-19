/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// CommandExecutor executa comandos do sistema de forma segura
type CommandExecutor struct {
	logger    *zap.Logger
	validator *CommandValidator
}

// NewCommandExecutor cria uma nova instância do executor
func NewCommandExecutor(logger *zap.Logger) *CommandExecutor {
	return &CommandExecutor{
		logger:    logger,
		validator: NewCommandValidator(logger),
	}
}

// ExecutionResult contém o resultado de uma execução
type ExecutionResult struct {
	Command   string
	Output    string
	Error     string
	ExitCode  int
	Duration  time.Duration
	WasKilled bool
}

// Execute executa um comando e retorna o resultado
func (e *CommandExecutor) Execute(ctx context.Context, command string, interactive bool) (*ExecutionResult, error) {
	// Validar comando
	if err := e.validator.ValidateCommand(command); err != nil {
		e.logger.Warn("Comando inválido", zap.String("command", command), zap.Error(err))
		return &ExecutionResult{
			Command: command,
			Error:   err.Error(),
		}, err
	}

	// --- LÓGICA DE SELEÇÃO DE SHELL E FLAG ---
	shell := os.Getenv("SHELL")
	shellFlag := "-c" // Padrão Unix (bash, zsh, sh)

	if shell == "" {
		if runtime.GOOS == "windows" {
			shell = "powershell.exe" // Fallback seguro para Windows
		} else {
			shell = "/bin/sh"
		}
	}

	// Ajuste da flag baseado no binário do shell
	if runtime.GOOS == "windows" {
		lowerShell := strings.ToLower(shell)
		if strings.Contains(lowerShell, "powershell") || strings.Contains(lowerShell, "pwsh") {
			shellFlag = "-Command"
		} else if strings.Contains(lowerShell, "cmd") {
			shellFlag = "/C"
		} else if strings.Contains(lowerShell, "bash") {
			// Git Bash no Windows usa -c
			shellFlag = "-c"
		}
	}
	// ------------------------------------------

	e.logger.Debug("Executando comando",
		zap.String("command", command),
		zap.String("shell", shell),
		zap.String("flag", shellFlag), // Log da flag
		zap.Bool("interactive", interactive))

	// Passamos a flag para as funções internas
	if interactive {
		return e.executeInteractive(ctx, shell, shellFlag, command)
	}

	return e.executeNonInteractive(ctx, shell, shellFlag, command)
}

// executeNonInteractive executa comando capturando saída
func (e *CommandExecutor) executeNonInteractive(ctx context.Context, shell, shellFlag, command string) (*ExecutionResult, error) {
	start := time.Now()
	result := &ExecutionResult{Command: command}

	cmd := exec.CommandContext(ctx, shell, shellFlag, command)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = os.Stdin

	err := cmd.Run()
	result.Duration = time.Since(start)

	result.Output = utils.SanitizeSensitiveText(stdout.String())
	result.Error = utils.SanitizeSensitiveText(stderr.String())

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				result.ExitCode = status.ExitStatus()
			}
		}
	}

	e.logger.Debug("Comando não-interativo executado",
		zap.String("command", command),
		zap.Int("exit_code", result.ExitCode),
		zap.Duration("duration", result.Duration))

	return result, err
}

// executeInteractive executa comando interativo
func (e *CommandExecutor) executeInteractive(ctx context.Context, shell, shellFlag, command string) (*ExecutionResult, error) {
	start := time.Now()
	result := &ExecutionResult{Command: command}

	fmt.Println(i18n.T("agent.executor.interactive_mode_header"))
	fmt.Println(i18n.T("agent.executor.interactive_mode_info"))
	fmt.Println(i18n.T("agent.executor.interactive_mode_exit_tip"))
	fmt.Println("----------------------------------------------")

	if runtime.GOOS != "windows" {
		saneCmd := exec.Command("stty", "sane")
		saneCmd.Stdin = os.Stdin
		if err := saneCmd.Run(); err != nil {
			e.logger.Warn(i18n.T("agent.executor.fail_restore_terminal"), zap.Error(err))
		}
	}

	// Configuração de perfil do shell (bashrc/zshrc)
	shellConfigPath := e.getShellConfigPath(shell)
	var shellCommand string

	// No Windows, a lógica de 'source' não funciona igual. Simplificamos para executar direto.
	if runtime.GOOS == "windows" {
		shellCommand = command
	} else {
		if shellConfigPath != "" {
			shellCommand = fmt.Sprintf("source %s 2>/dev/null || true; %s", shellConfigPath, command)
		} else {
			shellCommand = command
		}
	}

	cmd := exec.CommandContext(ctx, shell, shellFlag, shellCommand)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	result.Duration = time.Since(start)

	fmt.Println("\n" + i18n.T("agent.executor.interactive_mode_footer"))

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				result.ExitCode = status.ExitStatus()
			}
		}
		result.Error = err.Error()
	}

	e.logger.Debug("Comando interativo executado",
		zap.String("command", command),
		zap.Int("exit_code", result.ExitCode),
		zap.Duration("duration", result.Duration))

	return result, err
}

// getShellConfigPath retorna o caminho do arquivo de configuração do shell
func (e *CommandExecutor) getShellConfigPath(shell string) string {
	shellName := filepath.Base(shell)
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	switch shellName {
	case "bash":
		return filepath.Join(homeDir, ".bashrc")
	case "zsh":
		return filepath.Join(homeDir, ".zshrc")
	case "fish":
		return filepath.Join(homeDir, ".config", "fish", "config.fish")
	default:
		return ""
	}
}

// CaptureOutput executa comando e captura apenas a saída (para uso interno)
func (e *CommandExecutor) CaptureOutput(ctx context.Context, shell string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, shell, args...)
	cmd.Stdin = os.Stdin

	output, err := cmd.CombinedOutput()
	return output, err
}
