/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package agent

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// CommandValidator valida comandos antes da execução
type CommandValidator struct {
	logger            *zap.Logger
	dangerousPatterns []*regexp.Regexp
	extraDenyPatterns []*regexp.Regexp
	allowSudo         bool
}

// NewCommandValidator cria uma nova instância do validador
func NewCommandValidator(logger *zap.Logger) *CommandValidator {
	validator := &CommandValidator{
		logger:    logger,
		allowSudo: strings.EqualFold(os.Getenv("CHATCLI_AGENT_ALLOW_SUDO"), "true"),
	}

	// Padrões perigosos padrão
	defaultPatterns := []string{
		`(?i)rm\s+-rf\s+`,
		`(?i)rm\s+--no-preserve-root`,
		`(?i)dd\s+if=`,
		`(?i)mkfs\w*\s+`,
		`(?i)shutdown(\s+|$)`,
		`(?i)reboot(\s+|$)`,
		`(?i)init\s+0`,
		`(?i)curl\s+[^\|;]*\|\s*sh`,
		`(?i)wget\s+[^\|;]*\|\s*sh`,
		`(?i)curl\s+[^\|;]*\|\s*bash`,
		`(?i)wget\s+[^\|;]*\|\s*bash`,
		`(?i)\bsudo\b.*`,
		`(?i)\bdrop\s+database\b`,
		`(?i)\bmkfs\b`,
		`(?i)\buserdel\b`,
		`(?i)\bchmod\s+777\s+/.*`,
	}

	for _, pattern := range defaultPatterns {
		if re, err := regexp.Compile(pattern); err == nil {
			validator.dangerousPatterns = append(validator.dangerousPatterns, re)
		}
	}

	// Carregar denylist customizada
	if denylist := os.Getenv("CHATCLI_AGENT_DENYLIST"); denylist != "" {
		for _, pattern := range strings.Split(denylist, ";") {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
				continue
			}
			if re, err := regexp.Compile(pattern); err == nil {
				validator.extraDenyPatterns = append(validator.extraDenyPatterns, re)
			}
		}
	}

	return validator
}

// IsDangerous verifica se um comando é potencialmente perigoso
func (v *CommandValidator) IsDangerous(cmd string) bool {
	// Verificar padrões padrão
	for _, pattern := range v.dangerousPatterns {
		if pattern.MatchString(cmd) {
			return true
		}
	}

	// Verificar denylist extra
	for _, pattern := range v.extraDenyPatterns {
		if pattern.MatchString(cmd) {
			return true
		}
	}

	// Verificar sudo se não permitido
	if !v.allowSudo && regexp.MustCompile(`(?i)\bsudo\b`).MatchString(cmd) {
		return true
	}

	return false
}

// ValidateCommand valida um comando antes da execução
func (v *CommandValidator) ValidateCommand(cmd string) error {
	if strings.TrimSpace(cmd) == "" {
		return errors.New(i18n.T("agent.validator.empty_command"))
	}

	if v.IsDangerous(cmd) {
		// CORREÇÃO: Construir a string primeiro e passá-la como argumento para fmt.Errorf
		errorMsg := i18n.T("agent.validator.dangerous_command", cmd)
		return fmt.Errorf("%s", errorMsg)
	}

	return nil
}

// IsLikelyInteractive verifica se um comando provavelmente é interativo
func (v *CommandValidator) IsLikelyInteractive(cmd string) bool {
	interactiveCommands := []string{
		"top", "htop", "nettop", "iotop", "vi", "vim", "nano", "emacs", "less",
		"more", "tail -f", "watch", "ssh", "mysql", "psql", "sqlite3", "python",
		"ipython", "node", "irb", "R", "mongo", "redis-cli", "sqlplus", "ftp",
		"sftp", "telnet", "screen", "tmux", "ncdu", "mc", "ranger", "irssi",
		"weechat", "mutt", "lynx", "links", "w3m", "docker exec -it", "kubectl exec -it",
		"terraform", "ansible", "git", "gitk", "git gui", "git rebase -i",
		"kubectl", "helm", "oc", "minikube", "vagrant", "packer",
		"terraform console", "gcloud", "aws", "az", "pulumi", "pulumi up",
		"npm", "yarn", "pnpm", "composer", "bundle", "cargo",
	}

	cmdLower := strings.ToLower(cmd)
	cmdFields := strings.Fields(cmdLower)

	// Verificar comandos conhecidos
	for _, interactive := range interactiveCommands {
		if strings.HasPrefix(cmdLower, interactive+" ") || cmdLower == interactive {
			return true
		}
	}

	interactiveFlags := map[string]bool{
		"-i":            true,
		"--interactive": true,
		"-t":            true,
		"--tty":         true,
	}

	for _, field := range cmdFields {
		if interactiveFlags[field] {
			return true
		}
	}

	return false
}
