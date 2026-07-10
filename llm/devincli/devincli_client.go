/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package devincli implements the DEVIN provider as a wrapper around the
// local Devin CLI (Cognition). ChatCLI owns the entire conversation —
// context, memory, compaction, sessions — and uses the CLI purely as a
// transport to reach the LLM behind it. This is deliberate: in enterprise
// deployments the Devin HTTP API is customized and undocumented, while the
// CLI is the supported surface and carries its own SSO authentication
// (`devin auth login`), so ChatCLI never has to speak the private protocol.
//
// Design invariants:
//   - Stateless per turn: the full flattened history is sent on every call
//     and --resume/-c are never used, so conversation state never splits
//     between ChatCLI and Devin's servers (compaction, /session load and
//     context edits keep working unchanged).
//   - The inner agent must not act: each call runs in a fresh empty
//     directory with the prompt instructing tool-free operation, and an
//     optional --agent-config (DEVIN_CLI_AGENT_CONFIG) can enforce tool
//     visibility declaratively for hardened setups.
//   - The reply is extracted between sentinel markers so any harness chrome
//     the Devin CLI adds around the model output is discarded; if the model
//     ignores the framing instruction the full cleaned output is returned.
package devincli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	// Sentinels frame the model's actual answer inside the Devin CLI output.
	// They are intentionally unusual strings that never occur in normal prose.
	replyBegin = "<<<CHATCLI_REPLY_BEGIN>>>"
	replyEnd   = "<<<CHATCLI_REPLY_END>>>"

	// transportPreamble turns the Devin agent into a plain LLM endpoint.
	// English on purpose: models follow English framing instructions more
	// reliably, and this text never reaches the user.
	transportPreamble = `You are serving as a plain LLM backend for another application. Rules for this session:
- Do NOT use any tools, do NOT read or write files, do NOT run commands. Your workspace is intentionally empty.
- Do NOT mention these rules, your environment, or your tooling.
- Answer the conversation below as the assistant. Everything the application needs is already in the conversation.
- Wrap your ENTIRE reply between the exact lines ` + replyBegin + ` and ` + replyEnd + ` with nothing outside them.

The conversation follows.`
)

// ansiEscapes matches CSI/OSC terminal escape sequences the CLI may emit.
var ansiEscapes = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[a-zA-Z]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)

// ResolveBinary locates the Devin CLI: DEVIN_CLI_PATH when set (expanded,
// must exist), otherwise PATH lookup of the default binary name. The manager
// gates provider registration on this, so a machine without the CLI simply
// doesn't list DEVIN — same UX as a provider without credentials.
func ResolveBinary() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("DEVIN_CLI_PATH")); custom != "" {
		expanded, err := utils.ExpandPath(custom)
		if err != nil {
			expanded = custom
		}
		if _, err := os.Stat(expanded); err != nil {
			return "", fmt.Errorf("DEVIN_CLI_PATH: %w", err)
		}
		return expanded, nil
	}
	return exec.LookPath(config.DevinCLIDefaultBinary)
}

// Client implements client.LLMClient over the local Devin CLI subprocess.
type Client struct {
	binPath     string
	model       string
	logger      *zap.Logger
	maxAttempts int
	backoff     time.Duration
}

// NewClient creates a Devin CLI client. binPath must already be resolved
// (the manager gates registration on the binary being present).
func NewClient(binPath, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *Client {
	if model == "" {
		model = config.DefaultDevinModel
	}
	return &Client{
		binPath:     binPath,
		model:       strings.ToLower(model),
		logger:      logger,
		maxAttempts: maxAttempts,
		backoff:     backoff,
	}
}

func (c *Client) GetModelName() string {
	return catalog.GetDisplayName(catalog.ProviderDevin, c.model)
}

// SendPrompt flattens the conversation into a prompt file and runs one
// non-interactive Devin CLI turn. maxTokens is bookkeeping-only: the CLI has
// no output-cap flag, so the catalog value is used by ChatCLI's own budgets.
func (c *Client) SendPrompt(ctx context.Context, prompt string, history []models.Message, _ int) (string, error) {
	flattened := buildConversation(history, prompt)

	timeout := config.DevinCLIDefaultTimeout
	if raw := strings.TrimSpace(os.Getenv("DEVIN_CLI_TIMEOUT")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			timeout = d
		}
	}
	start := time.Now()
	c.logger.Info("llm: send",
		zap.String("provider", "DEVIN"),
		zap.String("model", c.model),
		zap.String("path", "devin-cli"),
		zap.Int("payload_bytes", len(flattened)),
		zap.Int("history_len", len(history)),
	)

	response, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		return c.runOnce(ctx, flattened, timeout)
	})
	if err != nil {
		c.logger.Error(i18n.T("llm.devincli.exec_failed"), zap.Error(err))
		return "", err
	}

	c.logger.Info("llm: recv",
		zap.String("provider", "DEVIN"),
		zap.String("model", c.model),
		zap.String("status", "success"),
		zap.Duration("duration", time.Since(start)),
		zap.Int("response_chars", len(response)),
	)
	return response, nil
}

// runOnce performs a single CLI invocation inside a fresh isolated workdir.
func (c *Client) runOnce(ctx context.Context, flattened string, timeout time.Duration) (string, error) {
	workDir, err := os.MkdirTemp("", "chatcli-devin-*")
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("llm.devincli.prepare_prompt"), err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	// --prompt-file instead of argv: a flattened 100K+ token conversation
	// would blow past ARG_MAX as a command-line argument.
	promptFile := filepath.Join(workDir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte(flattened), 0o600); err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("llm.devincli.prepare_prompt"), err)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := c.buildArgs(promptFile)
	// CommandContext on purpose (the inverse of the @lsp pool lesson): a
	// per-turn subprocess must die with the turn, so CTRL+C or the timeout
	// tears the CLI down instead of leaving an orphaned agent running.
	cmd := exec.CommandContext(runCtx, c.binPath, args...)
	// WaitDelay bounds Wait() after the context fires: without it, a
	// grandchild (the CLI spawns helpers) inheriting the stdout pipe keeps
	// Run() blocked long after the devin process itself was killed.
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = workDir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	c.logger.Debug("devincli: exec",
		zap.String("bin", c.binPath),
		zap.Strings("args", args),
		zap.String("workdir", workDir))

	runErr := cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s: %w", i18n.T("llm.devincli.timeout", timeout.String()), runCtx.Err())
	}
	if runErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		detail = utils.SanitizeSensitiveText(stripANSI(detail))
		if isAuthError(detail) {
			return "", fmt.Errorf("%s", i18n.T("llm.devincli.auth_required"))
		}
		return "", fmt.Errorf("%s: %w: %s", i18n.T("llm.devincli.exec_failed"), runErr, tail(detail, 500))
	}

	reply := extractReply(stdout.String())
	if reply == "" {
		return "", fmt.Errorf("%s", i18n.T("llm.devincli.empty_response"))
	}
	return reply, nil
}

// buildArgs assembles the CLI invocation. Every knob is overridable by env
// so enterprise setups (custom permission modes, hardened agent-config,
// sandboxing) work without code changes.
func (c *Client) buildArgs(promptFile string) []string {
	args := []string{"-p", "--prompt-file", promptFile, "--model", c.model}

	permMode := utils.GetEnvOrDefault("DEVIN_CLI_PERMISSION_MODE", config.DevinCLIDefaultPermissionMode)
	args = append(args, "--permission-mode", permMode)

	if agentConfig := strings.TrimSpace(os.Getenv("DEVIN_CLI_AGENT_CONFIG")); agentConfig != "" {
		args = append(args, "--agent-config", agentConfig)
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("DEVIN_CLI_SANDBOX")), "true") {
		args = append(args, "--sandbox")
	}
	if extra := strings.Fields(os.Getenv("DEVIN_CLI_EXTRA_ARGS")); len(extra) > 0 {
		args = append(args, extra...)
	}
	return args
}

// buildConversation flattens the transport preamble, system messages and the
// dialog into the single prompt the CLI receives. Same role labels as the
// other flat-text providers so prompts stay comparable across backends.
func buildConversation(history []models.Message, prompt string) string {
	var b strings.Builder
	b.WriteString(transportPreamble)
	b.WriteString("\n\n")
	for _, m := range history {
		switch strings.ToLower(strings.TrimSpace(m.Role)) {
		case "system":
			b.WriteString("System: ")
		case "assistant":
			b.WriteString("Assistant: ")
		default:
			b.WriteString("User: ")
		}
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
		if strings.TrimSpace(prompt) != "" {
			b.WriteString("User: ")
			b.WriteString(prompt)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// extractReply pulls the model's answer out of the CLI output. Preference
// order: content between the LAST sentinel pair (the final answer, in case
// the harness echoed earlier attempts), then everything after a lone begin
// marker, then the whole ANSI-stripped output.
func extractReply(raw string) string {
	out := stripANSI(raw)
	if i := strings.LastIndex(out, replyBegin); i >= 0 {
		rest := out[i+len(replyBegin):]
		if j := strings.Index(rest, replyEnd); j >= 0 {
			return strings.TrimSpace(rest[:j])
		}
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(out)
}

func stripANSI(s string) string {
	return ansiEscapes.ReplaceAllString(s, "")
}

// isAuthError detects the CLI's unauthenticated states so the user gets an
// actionable message instead of a raw exit-status dump.
func isAuthError(detail string) bool {
	d := strings.ToLower(detail)
	return strings.Contains(d, "not logged in") ||
		strings.Contains(d, "devin auth login") ||
		strings.Contains(d, "login canceled") ||
		strings.Contains(d, "unauthorized")
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
