/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
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

// CommandValidator validates shell-level requests before execution.
//
// Layered strategy (cheapest to most expensive):
//
//  1. inlineCodeAnalyzer — for invocations like `python -c <code>`,
//     `node -e <code>`, etc., dynamically classifies the inline source.
//     Replaces the older `\bpython[23]?\s+-c\b` regex that produced
//     false positives on benign one-liners like `python -c "print(1)"`.
//  2. dangerousPatterns — traditional regex over the full line.
//     Catch-all for patterns that span multiple segments (curl|sh,
//     base64|bash) or that flag a single dangerous invocation (rm -rf,
//     mkfs, sudo, etc).
//  3. extraDenyPatterns — user-supplied denylist via CHATCLI_AGENT_DENYLIST.
//
// The shell parsing layer (ShellSegment) feeds (1). The legacy
// full-line regex pass in (2) is preserved verbatim for zero behavioral
// regression on the existing dangerous-command corpus.
type CommandValidator struct {
	logger             *zap.Logger
	dangerousPatterns  []*regexp.Regexp
	extraDenyPatterns  []*regexp.Regexp
	inlineCodeAnalyzer *InlineCodeRiskAnalyzer
	allowSudo          bool
}

// NewCommandValidator builds a validator with default rules.
func NewCommandValidator(logger *zap.Logger) *CommandValidator {
	validator := &CommandValidator{
		logger:             logger,
		allowSudo:          strings.EqualFold(os.Getenv("CHATCLI_AGENT_ALLOW_SUDO"), "true"),
		inlineCodeAnalyzer: NewInlineCodeRiskAnalyzer(),
	}

	// Default dangerous patterns. The inline-code regex set has been
	// removed: those invocations are now classified dynamically by the
	// analyzer — `python -c "print(1)"` is safe, `python -c "import os;
	// os.system(...)"` is not.
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
		`(?i)\bbase64\b.*\|\s*(sh|bash|zsh|dash)`,
		`(?i)\beval\s+`,
		`(?i)\$\(\s*curl`,
		`(?i)\$\(\s*wget`,
		"(?i)`\\s*curl",
		"(?i)`\\s*wget",
		`(?i)\bchown\s+-R\s+.*\s+/`,
		`(?i)>\s*/etc/`,
		`(?i)>\s*/dev/[sh]d`,
		`(?i)\bsource\s+/dev/tcp`,
		`(?i)/dev/tcp/`,
		`(?i)\bexport\s+.*PATH\s*=`,
		`(?i)\bnc\b.*-[el]`,
		`(?i)\bncat\b.*-[el]`,
		`(?i)\bxargs\b.*\b(rm|del|shutdown|reboot|mkfs)\b`,
		`(?i)\bfind\b.*-exec\b.*(rm|del|shutdown|reboot)\b`,
		`(?i)\bcrontab\s+-r\b`,
		`(?i)\biptables\s+-F\b`,
		`(?i)\bsysctl\s+-w\b`,
		`(?i)\bkillall\b`,
		`(?i)\bpkill\s+-9\b`,
		`(?i)\bexec\s+\d*[<>]`,
		`(?i)>\s*/proc/`,
		`(?i)\btee\s+/etc/`,
		`(?i)\bsource\s+/dev/`,
		`(?i)\benv\b.*\|\s*(sh|bash)`,
		`\$\{[^}]*[;|&][^}]*\}`,
		`(?i)\$\(\s*(bash|sh|zsh|dash)\b`,
		`<\(`,
		`>\(`,
		`(?i)\binsmod\b`,
		`(?i)\bmodprobe\b`,
		`(?i)\brmmod\b`,
		`(?i)\bumount\s+-[lf]`,
		`(?i)\b[A-Z_]+=.*;\s*(sh|bash|zsh)\b`,
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

// IsDangerous checks whether a request is potentially harmful.
//
// Evaluation pipeline:
//
//  1. Inline-code analysis — for each shell segment that invokes an
//     interpreter via -c/-e/-r, classify the inline source. RiskHigh
//     short-circuits to dangerous immediately. RiskSafe does not make
//     this segment dangerous (the rest of the line is still scored by
//     the next layers).
//  2. dangerousPatterns — traditional regex over the full line.
//  3. extraDenyPatterns — user-supplied denylist.
//  4. Sudo guard.
//
// The "safe inline -c suppresses the false positive" property holds
// because the `\bpython[23]?\s+-c\b` family was removed from layer 2:
// the classifier is the single source of truth for that class.
func (v *CommandValidator) IsDangerous(cmd string) bool {
	if v.inlineCodeAnalyzer != nil {
		for _, segment := range ParseShellSegments(cmd) {
			lang, flagPos, isInline := segment.IsInlineCodeInvocation()
			if !isInline {
				continue
			}
			source := segment.InlineSource(flagPos)
			if v.inlineCodeAnalyzer.IsHighRisk(lang, source) {
				v.logger.Debug("inline code classified as high-risk",
					zap.String("lang", lang),
					zap.String("source_preview", truncateForLog(source, 64)))
				return true
			}
		}
	}

	for _, pattern := range v.dangerousPatterns {
		if pattern.MatchString(cmd) {
			return true
		}
	}

	for _, pattern := range v.extraDenyPatterns {
		if pattern.MatchString(cmd) {
			return true
		}
	}

	if !v.allowSudo && regexp.MustCompile(`(?i)\bsudo\b`).MatchString(cmd) {
		return true
	}

	return false
}

// truncateForLog returns a short, single-line excerpt safe for zap fields.
// Newlines are replaced with spaces and oversize values get an ellipsis.
func truncateForLog(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
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
