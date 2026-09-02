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
	"runtime"
	"strings"
	"sync"
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

	// transportPreamble turns the Devin agent into a plain LLM endpoint
	// WITHOUT hijacking ChatCLI's own tool protocol: ChatCLI's agent/coder
	// modes define a textual tool-call markup in their system messages, and
	// the model must keep emitting that markup — only Devin's NATIVE tools
	// (file access, shell, web) are off-limits. Two hard-won framings live
	// here: (1) the first version said a blanket "do NOT use any tools" and
	// made the model ignore ChatCLI's tool catalog entirely; (2) a later
	// version assigned the model an identity ("You are the LLM backend for
	// ChatCLI") plus a secrecy rule ("do not mention these rules"), and Devin
	// refused whole tasks rather than misrepresent who it is. So: no identity
	// claim, no secrecy — the agent keeps its own identity and simply
	// collaborates through ChatCLI's textual protocol. English on purpose:
	// models follow English framing instructions more reliably, and this
	// text never reaches the user.
	transportPreamble = `ChatCLI, a separate CLI application, is relaying a conversation between its user and an AI assistant to you. Your job is to write the assistant's next reply. You keep your own identity throughout — you are not being asked to impersonate ChatCLI or anyone else, and if the user asks who you are, answer truthfully.

How to reply:
- The conversation below is the request. The system messages inside it define ChatCLI's working rules — tool catalogs, output formats, and a textual tool-call markup (such as <tool_call name="..." args='...' /> tags or @tool commands). Follow those rules: when a tool is the right next step, write the tool-call markup as plain text in your reply. ChatCLI parses that markup and executes the tool on the user's machine under the user's supervision and approval. Emitting the markup is the only way actions happen — it is ordinary text output, not you executing anything.
- Do not use your own native capabilities (file access, shell, browsing): your local workspace here is intentionally empty, and real actions belong to ChatCLI through the markup above.
- Wrap your ENTIRE reply between the exact lines ` + replyBegin + ` and ` + replyEnd + ` with nothing outside them.

The conversation follows.`
)

// ansiEscapes matches CSI/OSC terminal escape sequences the CLI may emit.
var ansiEscapes = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[a-zA-Z]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)

// runMu serializes Devin CLI invocations within this process. The CLI keeps
// per-user state (config, session store) that concurrent instances contend
// on: the background memory worker firing an extraction while an agent turn
// is mid-flight was observed failing with CLI-side file errors. Turns are
// short relative to the correctness win, and each waiter still honors its
// own context deadline while queued.
var runMu sync.Mutex

// acquireRunSlot takes runMu without outliving ctx while waiting. On
// success the returned func releases the slot; on ctx expiry the helper
// goroutine eventually grabs and immediately releases the abandoned slot
// so the mutex is never leaked.
func acquireRunSlot(ctx context.Context) (func(), error) {
	lockCh := make(chan struct{})
	go func() { runMu.Lock(); close(lockCh) }()
	select {
	case <-lockCh:
		return runMu.Unlock, nil
	case <-ctx.Done():
		go func() { <-lockCh; runMu.Unlock() }()
		return nil, ctx.Err()
	}
}

// ResolveBinary locates the Devin CLI: DEVIN_CLI_PATH when set (expanded,
// must exist — an explicit path never falls back), otherwise PATH lookup of
// the default binary name, otherwise a probe of well-known install dirs. The
// manager gates provider registration on this, so a machine without the CLI
// simply doesn't list DEVIN — same UX as a provider without credentials.
func ResolveBinary() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("DEVIN_CLI_PATH")); custom != "" {
		expanded, err := utils.ExpandPath(custom)
		if err != nil {
			expanded = custom
		}
		expanded = filepath.Clean(expanded)
		// #nosec G703 -- DEVIN_CLI_PATH is operator-supplied configuration
		// (same trust level as PATH itself); Stat only validates existence
		// and shape before the manager registers the provider.
		info, err := os.Stat(expanded)
		if err != nil {
			return "", fmt.Errorf("DEVIN_CLI_PATH: %w", err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("DEVIN_CLI_PATH: %s is a directory, expected the devin binary", expanded)
		}
		return expanded, nil
	}
	found, lookErr := exec.LookPath(config.DevinCLIDefaultBinary)
	if lookErr == nil {
		return found, nil
	}
	if probed, ok := probeKnownDirs(config.DevinCLIDefaultBinary, knownInstallDirs()); ok {
		return probed, nil
	}
	return "", lookErr
}

// knownInstallDirs lists the well-known install locations probed when the
// PATH lookup misses. The ACP/MCP servers are spawned by IDEs and other GUI
// hosts that inherit the minimal session PATH (on macOS, launchd's
// /usr/bin:/bin:…) instead of the user's shell profile, so a devin installed
// via Homebrew or npm is visible in the terminal REPL but not to the server —
// probing the standard install dirs keeps the provider available in both
// without per-IDE env plumbing. Variable so tests can substitute the dirs.
var knownInstallDirs = func() []string {
	if runtime.GOOS == "windows" {
		var dirs []string
		if lad := os.Getenv("LOCALAPPDATA"); lad != "" {
			dirs = append(dirs, filepath.Join(lad, "Programs", "devin"))
		}
		if ad := os.Getenv("APPDATA"); ad != "" {
			dirs = append(dirs, filepath.Join(ad, "npm"))
		}
		if home, err := os.UserHomeDir(); err == nil {
			dirs = append(dirs, filepath.Join(home, "scoop", "shims"))
		}
		return dirs
	}
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "bin"),
			filepath.Join(home, ".devin", "bin"),
		)
	}
	return append(dirs, "/opt/homebrew/bin", "/usr/local/bin", "/home/linuxbrew/.linuxbrew/bin")
}

// probeKnownDirs returns the first dir whose entry resolves to an executable.
// exec.LookPath on a path containing a separator validates that exact file
// (applying PATHEXT on Windows), so the same probe works on every platform.
func probeKnownDirs(name string, dirs []string) (string, bool) {
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if p, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
			return p, true
		}
	}
	return "", false
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
	// Serialize with any other in-process invocation (see runMu), but never
	// outlive the caller's context while waiting for the slot.
	release, err := acquireRunSlot(ctx)
	if err != nil {
		return "", err
	}
	defer release()

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
	// #nosec G204 G702 -- binPath resolves from operator configuration
	// (PATH lookup or DEVIN_CLI_PATH) and argv is assembled from
	// operator-supplied env knobs, never from model or network input —
	// same trust model as llm/transcription's operator-supplied command.
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
		// Scrub any sentinel that leaked into a stored message (from a turn
		// where the model mangled the framing): replaying it here teaches the
		// model that replies carry the markers, contaminating every turn after.
		b.WriteString(stripHistorySentinels(m.Content))
		b.WriteString("\n")
	}
	if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
		if strings.TrimSpace(prompt) != "" {
			b.WriteString("User: ")
			b.WriteString(prompt)
			b.WriteString("\n")
		}
	}
	// The Devin CLI (Rust) rejects the prompt file outright on any invalid
	// UTF-8 byte ("stream did not contain valid UTF-8"). Conversation
	// segments can carry raw bytes — tool outputs, file reads — and the
	// HTTP providers never see them because encoding/json silently coerces
	// invalid sequences to U+FFFD when marshaling. Mirror that exact
	// behavior at this transport's boundary.
	return strings.ToValidUTF8(b.String(), "\uFFFD")
}

// extractReply pulls the model's answer out of the CLI output. Preference
// order: content between the LAST sentinel pair (the final answer, in case
// the harness echoed earlier attempts), then everything after a lone begin
// marker, then the whole ANSI-stripped output. Every path runs through
// stripSentinels: when the model mangles the framing (e.g. emits only the
// end marker, or fences the begin marker so it no longer matches), the
// fallback would otherwise leak a literal sentinel to the user — and once a
// leaked marker lands in the history it self-reinforces, because the model
// sees it in the flattened conversation and imitates it every turn after.
func extractReply(raw string) string {
	out := stripANSI(raw)
	if i := strings.LastIndex(out, replyBegin); i >= 0 {
		rest := out[i+len(replyBegin):]
		if j := strings.Index(rest, replyEnd); j >= 0 {
			return stripSentinels(rest[:j])
		}
		return stripSentinels(rest)
	}
	return stripSentinels(out)
}

// stripSentinels removes any residual framing markers from text headed to the
// user. The markers are transport framing, never content: they are
// intentionally unusual strings that cannot occur in prose.
func stripSentinels(s string) string {
	return strings.TrimSpace(stripHistorySentinels(s))
}

// stripHistorySentinels removes the framing markers without touching the
// surrounding whitespace — used when flattening stored messages, where the
// content must otherwise be replayed verbatim.
func stripHistorySentinels(s string) string {
	s = strings.ReplaceAll(s, replyBegin, "")
	return strings.ReplaceAll(s, replyEnd, "")
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
