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
		// Retorna um resultado de erro imediatamente
		return &ExecutionResult{
			Command: command,
			Error:   err.Error(),
		}, err
	}

	// Obter shell
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	e.logger.Debug("Executando comando",
		zap.String("command", command),
		zap.String("shell", shell),
		zap.Bool("interactive", interactive))

	if interactive {
		return e.executeInteractive(ctx, shell, command)
	}

	return e.executeNonInteractive(ctx, shell, command)
}

// executeNonInteractive executa comando capturando saída
func (e *CommandExecutor) executeNonInteractive(ctx context.Context, shell, command string) (*ExecutionResult, error) {
	start := time.Now() // A medição de tempo começa aqui
	result := &ExecutionResult{Command: command}

	cmd := exec.CommandContext(ctx, shell, "-c", command)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = os.Stdin

	err := cmd.Run()
	result.Duration = time.Since(start) // E termina aqui

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
func (e *CommandExecutor) executeInteractive(ctx context.Context, shell, command string) (*ExecutionResult, error) {
	start := time.Now() // A medição de tempo começa aqui
	result := &ExecutionResult{Command: command}

	fmt.Println(i18n.T("agent.executor.interactive_mode_header"))
	fmt.Println(i18n.T("agent.executor.interactive_mode_info"))
	fmt.Println(i18n.T("agent.executor.interactive_mode_exit_tip"))
	fmt.Println("----------------------------------------------")

	saneCmd := exec.Command("stty", "sane")
	saneCmd.Stdin = os.Stdin
	if err := saneCmd.Run(); err != nil {
		e.logger.Warn(i18n.T("agent.executor.fail_restore_terminal"), zap.Error(err))
	}

	shellConfigPath := e.getShellConfigPath(shell)
	var shellCommand string
	if shellConfigPath != "" {
		shellCommand = fmt.Sprintf("source %s 2>/dev/null || true; %s", shellConfigPath, command)
	} else {
		shellCommand = command
	}

	cmd := exec.CommandContext(ctx, shell, "-c", shellCommand)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	result.Duration = time.Since(start) // E termina aqui

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
