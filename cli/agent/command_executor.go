/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
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
	logger        *zap.Logger
	validator     *CommandValidator
	allowlist     *CommandAllowlist
	readValidator *SensitiveReadPaths
}

// NewCommandExecutor cria uma nova instância do executor
func NewCommandExecutor(logger *zap.Logger) *CommandExecutor {
	return &CommandExecutor{
		logger:        logger,
		validator:     NewCommandValidator(logger),
		allowlist:     NewCommandAllowlist(),
		readValidator: NewSensitiveReadPaths(),
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
	// Security (H8): Allowlist-based command validation (defense in depth)
	if e.allowlist.GetMode() == SecurityModeStrict {
		if allowed, _, reason := e.allowlist.IsAllowed(command); !allowed {
			e.logger.Warn("Command blocked by allowlist", zap.String("command", command), zap.String("reason", reason))
			return &ExecutionResult{
				Command: command,
				Error:   reason,
			}, fmt.Errorf("command blocked: %s (set CHATCLI_AGENT_SECURITY_MODE=permissive to use denylist fallback)", reason)
		}
	} else if e.allowlist.GetMode() == SecurityModePermissive {
		// In permissive mode, check allowlist first; if not allowed, fall through to denylist
		if allowed, _, _ := e.allowlist.IsAllowed(command); !allowed {
			e.logger.Debug("Command not in allowlist, falling through to denylist", zap.String("command", command))
		}
	}

	// Legacy denylist validation (always active as second layer)
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

	// Validate $SHELL against known shell binaries to prevent env manipulation
	if runtime.GOOS != "windows" {
		shellBase := filepath.Base(shell)
		allowedShells := map[string]bool{
			"sh": true, "bash": true, "zsh": true, "dash": true,
			"fish": true, "ksh": true, "csh": true, "tcsh": true,
		}
		if !allowedShells[shellBase] {
			e.logger.Warn("Unrecognized shell, falling back to /bin/sh",
				zap.String("shell", shell))
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

	cmd := exec.CommandContext(ctx, shell, shellFlag, command) //#nosec G204 G702 -- agent shell command, validated upstream by command_validator + policy_manager
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

	// Security (M13): Default to --noprofile --norc. Opt-in via CHATCLI_AGENT_SOURCE_SHELL_CONFIG=true.
	sourceShellConfig := strings.EqualFold(os.Getenv("CHATCLI_AGENT_SOURCE_SHELL_CONFIG"), "true")

	// No Windows, a lógica de 'source' não funciona igual. Simplificamos para executar direto.
	if runtime.GOOS == "windows" {
		shellCommand = command
	} else {
		if sourceShellConfig && shellConfigPath != "" {
			if err := e.validateShellConfig(shellConfigPath); err != nil {
				e.logger.Warn("Skipping shell config sourcing", zap.Error(err))
				shellConfigPath = ""
			}
			// Additional validation: file ownership must match current user
			if shellConfigPath != "" {
				if info, err := os.Stat(shellConfigPath); err == nil {
					// Check file size (reject > 1MB — shell configs shouldn't be that large)
					if info.Size() > 1024*1024 {
						e.logger.Warn("Shell config too large, skipping", zap.Int64("size", info.Size()))
						shellConfigPath = ""
					}
				}
			}
		} else {
			shellConfigPath = "" // Don't source shell config by default
		}

		if shellConfigPath != "" {
			shellCommand = fmt.Sprintf("source %s 2>/dev/null || true; %s", utils.ShellQuote(shellConfigPath), command)
		} else {
			shellCommand = command
		}
	}

	cmd := exec.CommandContext(ctx, shell, shellFlag, shellCommand) //#nosec G204 G702 -- agent interactive shell command, validated upstream

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

// validateShellConfig checks that a shell config file is safe to source.
// It rejects symlinks (which could point to malicious files) and
// world-writable files (which could be tampered with).
func (e *CommandExecutor) validateShellConfig(path string) error {
	if path == "" {
		return nil
	}

	info, err := os.Lstat(path)
	if err != nil {
		return err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("shell config %q is a symlink — refusing to source", path)
	}

	if runtime.GOOS != "windows" && info.Mode().Perm()&0002 != 0 {
		return fmt.Errorf("shell config %q is world-writable — refusing to source", path)
	}

	return nil
}

// CaptureOutput executa comando e captura apenas a saída (para uso interno)
func (e *CommandExecutor) CaptureOutput(ctx context.Context, shell string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, shell, args...) //#nosec G204 -- agent/CLI tool execution; commands validated by command_validator + policy_manager upstream
	cmd.Stdin = os.Stdin

	output, err := cmd.CombinedOutput()
	return output, err
}
