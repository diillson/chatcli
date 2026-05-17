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

// CommandValidator valida comandos antes da execução.
//
// Estratégia em camadas (do mais barato pro mais caro):
//
//  1. inlineCodeAnalyzer — para invocações como `python -c <code>`,
//     `node -e <code>`, etc., classifica dinamicamente o código inline.
//     Substitui as antigas regex `\bpython[23]?\s+-c\b` que falsavam-
//     positivo em `python -c "print(1)"`.
//  2. dangerousPatterns — regex tradicional aplicada à linha completa.
//     Catch-all para padrões que envolvem múltiplos segmentos (curl|sh,
//     base64|bash) ou que detectam um comando perigoso isolado (rm -rf,
//     mkfs, sudo, etc).
//  3. extraDenyPatterns — denylist customizada via CHATCLI_AGENT_DENYLIST.
//
// A camada de parsing shell (ShellSegment) é usada como input pra (1) e
// pode no futuro alimentar uma camada (4) de análise per-segmento. Hoje
// preserva o comportamento legado (regex on full line) pra zero regressão.
type CommandValidator struct {
	logger             *zap.Logger
	dangerousPatterns  []*regexp.Regexp
	extraDenyPatterns  []*regexp.Regexp
	inlineCodeAnalyzer *InlineCodeRiskAnalyzer
	allowSudo          bool
}

// NewCommandValidator cria uma nova instância do validador.
func NewCommandValidator(logger *zap.Logger) *CommandValidator {
	validator := &CommandValidator{
		logger:             logger,
		allowSudo:          strings.EqualFold(os.Getenv("CHATCLI_AGENT_ALLOW_SUDO"), "true"),
		inlineCodeAnalyzer: NewInlineCodeRiskAnalyzer(),
	}

	// Padrões perigosos padrão. Removidos os patterns de inline-code que
	// agora são classificados dinamicamente pelo analyzer — `python -c
	// "print(1)"` é seguro, `python -c "import os; os.system(...)"` não é.
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

// IsDangerous verifica se um comando é potencialmente perigoso.
//
// Pipeline de avaliação:
//
//  1. Inline-code analysis — para cada segmento do shell que invoca um
//     interpretador via -c/-e/-r, classifica o código inline. Se o
//     classifier diz RiskHigh, é dangerous imediatamente. Se diz RiskSafe,
//     a chamada não é dangerous *por causa* desse segmento (mas o resto
//     da linha ainda é avaliado pelas camadas seguintes).
//  2. dangerousPatterns — regex tradicional aplicada à linha completa.
//  3. extraDenyPatterns — denylist do usuário.
//  4. sudo guard.
//
// O critério "safe inline -c suprime o falso-positivo" funciona porque
// removemos as regex `\bpython[23]?\s+-c\b` da lista da camada 2: o
// classifier é a fonte da verdade pra essa classe de comandos.
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
func truncateForLog(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
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
