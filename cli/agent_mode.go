/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/hooks"
	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/metrics"
	"github.com/diillson/chatcli/cli/paste"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/persona"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
	"golang.org/x/term"
)

// AgentMode representa a funcionalidade de agente autônomo no ChatCLI
type AgentMode struct {
	cli                 *ChatCLI
	logger              *zap.Logger
	executor            *agent.CommandExecutor
	validator           *agent.CommandValidator
	contextManager      *agent.ContextManager
	taskTracker         *agent.TaskTracker
	executeCommandsFunc func(ctx context.Context, block agent.CommandBlock) (string, string)
	// runInflight is set to true while Run() is executing on this
	// instance. AgentMode mutates per-instance state heavily (history,
	// taskTracker, isCoderMode, isOneShot, qualityConfig, …) and grabs
	// the terminal/TUI in interactive mode, so concurrent Run() calls
	// race or deadlock. The scheduler bridge inspects this flag with
	// atomic.Bool.CompareAndSwap to fail-fast instead of deadlocking
	// when the user is mid-session in /agent or /coder.
	runInflight      atomic.Bool
	isCoderMode      bool
	isOneShot        bool
	coderBannerShown bool
	lastPolicyMatch  *coder.Rule
	// Métricas
	turnTimer      *metrics.Timer
	agentsLaunched int // total de sub-agents lançados na sessão
	toolCallsExecd int // total de tool calls executadas na sessão
	// Multi-Agent Orchestration
	agentDispatcher *workers.Dispatcher
	agentRegistry   *workers.Registry
	fileLockMgr     *workers.FileLockManager
	policyAdapter   *workerPolicyAdapter
	parallelMode    bool
	// Seven-pattern quality pipeline (Self-Refine, CoVe, Reflexion, …).
	// qualityPipeline is wired into the dispatcher; qualityConfig holds
	// the env-loaded snapshot so /config quality can render it without
	// re-parsing env vars on every invocation.
	qualityPipeline *quality.Pipeline
	qualityConfig   quality.Config
	// Centralized stdin reader for type-ahead queue support
	stdinLines   chan string   // all stdin lines flow through here
	stdinDone    chan struct{} // signals reader goroutine to stop
	stdinWg      sync.WaitGroup
	multilineBuf MultilineBuffer // ``` delimited multiline input

	// Skill hints captured at the start of Run() from auto-activated or
	// manually invoked skills. Applied to each LLM turn via ctx for the
	// duration of the agent loop. Cleared when the loop exits.
	skillModelHint  string
	skillEffortHint llmclient.SkillEffort

	// Session-scoped flag: true once we have warned the user that the
	// history is approaching likely corporate-proxy payload limits and
	// no explicit CHATCLI_MAX_PAYLOAD is configured. Prevents the warning
	// from being emitted every turn.
	proxyPayloadWarned bool

	// lastTurnToolResults holds the structured outcome of every tool
	// call executed in the most recent ReAct turn. Populated by the
	// batch loop alongside the legacy concatenation. Consumed by:
	//
	//   - Fase 3 orchestrator: feeds back into the next turn's partition.
	//   - Fase 5 provider adapters: emit tool_result blocks with
	//     is_error / errno per call instead of one fused user message.
	//   - Telemetry: per-tool duration and error_code aggregation.
	//
	// Kept on the struct (not a local) so debug commands and tests can
	// inspect it after a turn completes.
	lastTurnToolResults []agent.ToolResult
}

// splitStdinChunk consumes raw bytes from a stdin Read() call and returns
// the complete lines found (each terminated with a trailing '\n'). Bytes
// that don't yet form a full line are appended to lineBuf for the next
// chunk to finish.
//
// Both '\n' and '\r' end a line. Cooked TTYs deliver '\n' (ICRNL converts
// the user's CR), but a raw-mode TTY — or one left in a transient state
// after a TIOCSTI inject for park auto-resume — delivers '\r'. Recognising
// both keeps the security prompt responsive in either mode. CRLF pairs are
// collapsed into a single line (the trailing '\n' is consumed).
func splitStdinChunk(chunk []byte, lineBuf *strings.Builder) []string {
	var lines []string
	for i := 0; i < len(chunk); i++ {
		b := chunk[i]
		if b != '\n' && b != '\r' {
			lineBuf.WriteByte(b)
			continue
		}
		lines = append(lines, lineBuf.String()+"\n")
		lineBuf.Reset()
		if b == '\r' && i+1 < len(chunk) && chunk[i+1] == '\n' {
			i++
		}
	}
	return lines
}

// startStdinReader starts a goroutine that reads lines from stdin and sends
// them to the stdinLines channel. This centralizes all stdin reads in agent
// mode, enabling type-ahead queue support.
//
// Uses stdinPollReady (poll(2) on Unix, WaitForSingleObject on Windows) to
// check for available input before calling os.Stdin.Read. This ensures the
// goroutine never blocks for more than ~50ms, so it can check stdinDone and
// exit cleanly when agent mode ends — without requiring the user to press Enter.
func (a *AgentMode) startStdinReader() {
	a.stdinLines = make(chan string, 10)
	a.stdinDone = make(chan struct{})
	a.stdinWg.Add(1)

	go func() {
		defer a.stdinWg.Done()
		var lineBuf strings.Builder
		buf := make([]byte, 512)
		for {
			select {
			case <-a.stdinDone:
				return
			default:
			}

			// Poll stdin with 50ms timeout. On Unix (Linux/macOS) this uses
			// poll(2) which correctly handles TTY fds. On Windows this uses
			// WaitForSingleObject on the console input handle.
			if !stdinPollReady(50 * time.Millisecond) {
				continue // timeout — loop back and check stdinDone
			}

			// Data available — read won't block (on Unix).
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}

			lines := splitStdinChunk(buf[:n], &lineBuf)
			for _, rawLine := range lines {
				// Detect and clean paste content
				cleaned, pasteInfo := paste.DetectInLine(rawLine)
				if pasteInfo != nil {
					if pasteInfo.LineCount > 1 {
						fmt.Printf("  %s\n", i18n.T("paste.detected", pasteInfo.CharCount, pasteInfo.LineCount))
					} else {
						fmt.Printf("  %s\n", i18n.T("paste.detected.short", pasteInfo.CharCount))
					}
					rawLine = cleaned
				}

				line := strings.TrimSpace(rawLine)

				select {
				case <-a.stdinDone:
					return
				case a.stdinLines <- line:
				}
			}
		}
	}()
}

// stopStdinReader signals the stdin reader goroutine to stop and waits for it
// to exit. On Unix (Linux/macOS), the goroutine exits within ~50ms (one poll
// cycle). On Windows, it may be blocked in os.Stdin.Read after a
// WaitForSingleObject false positive; a safety timeout prevents indefinite
// blocking.
func (a *AgentMode) stopStdinReader() {
	if a.stdinDone != nil {
		close(a.stdinDone)

		// Wait with safety timeout. On Unix the goroutine exits in ~50ms.
		// On Windows it might be stuck in a blocking Read; don't wait forever.
		done := make(chan struct{})
		go func() {
			a.stdinWg.Wait()
			close(done)
		}()
		select {
		case <-done:
			// Clean exit
		case <-time.After(500 * time.Millisecond):
			// Goroutine stuck on blocking read (Windows edge case).
			// It will discard any data and exit on the next stdin input.
		}

		a.stdinLines = nil
		a.stdinDone = nil
	}
}

// readLine reads a single line from the centralized stdin reader.
// Falls back to direct stdin read if the reader is not active.
func (a *AgentMode) readLine() string {
	if a.stdinLines != nil {
		return <-a.stdinLines
	}
	// Fallback: direct stdin read
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

// readLineWithEditing reads a single line of user input for coder
// mode iteration. It delegates to go-prompt — the same readline used
// by chat mode — so behavior is identical: full terminal width,
// bracketed-paste handling (multi-line paste preserved instead of
// submitting on the first newline), arrow-key navigation, Ctrl+A/E,
// word movement, history-free single-shot input. Reusing the chat
// stack here is a deliberate UX choice: users should not have to
// learn a second editing model when the agent waits for their reply.
//
// Falls back to plain bufio.Reader when stdin isn't a TTY (piped
// input, CI), where go-prompt's raw-mode setup would fail.
func (a *AgentMode) readLineWithEditing() (string, error) {
	fd := int(os.Stdin.Fd()) // #nosec G115 -- Fd() returns uintptr, safe on all supported platforms

	if !term.IsTerminal(fd) {
		return readLinePlainFromReader(bufio.NewReader(os.Stdin)), nil
	}

	line := runWithCookedTerminalRestore(fd, a.readLineFromGoPrompt)
	return a.processInteractiveLine(line, bufio.NewReader(os.Stdin))
}

// runWithCookedTerminalRestore invokes fn while preserving the
// terminal's cooked-mode state across go-prompt's raw-mode usage.
//
// go-prompt switches stdin to raw mode internally and tries to
// restore on tearDown, but on macOS that cleanup is not 100%
// reliable — `icanon`/`icrnl` sometimes stay off, which makes any
// subsequent line-buffered read (e.g. the bufio reader used for
// multiline continuation lines) hang forever because Enter delivers
// '\r' instead of '\n'. Snapshotting before fn runs and forcing
// term.Restore afterwards is the cinto-+-suspensório fix.
//
// On non-TTY stdin (pipes, GetState error) the snapshot fails
// silently and fn still runs — there's nothing to restore. Extracted
// so the snapshot/invoke/restore dance is unit-testable with a pipe
// fd, without spinning up a real PTY.
func runWithCookedTerminalRestore(fd int, fn func() string) string {
	state, _ := term.GetState(fd)
	out := fn()
	if state != nil {
		_ = term.Restore(fd, state)
	}
	return out
}

// processInteractiveLine is the TTY-side post-prompt pipeline: take
// the line that go-prompt already returned, replay any captured
// bracketed-paste content, and either return the trimmed line or —
// if the user typed a multiline trigger — drain continuation lines
// from `multilineReader` until the matching delimiter.
//
// Kept as a separate function so paste replay and multiline dispatch
// are unit-testable without spinning up a real terminal. The function
// no longer reads anything itself — readLineWithEditing is the single
// point that touches stdin/go-prompt, which keeps the terminal-mode
// dance contained in one place.
func (a *AgentMode) processInteractiveLine(line string, multilineReader *bufio.Reader) (string, error) {
	// Mirror the chat-mode paste handling: when a large paste was
	// captured behind a placeholder, swap it back in. Always clear
	// lastPasteInfo so the next chat-mode prompt doesn't see a stale
	// notification from this coder iteration.
	line = a.applyPendingPasteInfo(line)

	trimmed := strings.TrimSpace(line)

	// Support multiline delimiter: if the user types "---", enter
	// multiline mode and accumulate until the matching delimiter.
	if isMultilineTrigger(trimmed) {
		return a.runMultilineSession(trimmed, multilineReader)
	}

	return trimmed, nil
}

// readLinePlainFromReader is the non-TTY fallback used when stdin is
// piped (CI, one-shot scripts). Pulled out as a top-level function so
// tests can drive it with any io.Reader without needing to swap
// os.Stdin.
func readLinePlainFromReader(reader *bufio.Reader) string {
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

// readLineFromGoPrompt isolates the go-prompt invocation so the
// surrounding logic (paste replay, multiline trigger detection) can
// be tested without spinning up a real terminal. Tests stub this by
// not calling readLineWithEditing directly; production code uses it
// as the one go-prompt-touching path.
func (a *AgentMode) readLineFromGoPrompt() string {
	pasteParser := paste.NewBracketedPasteParser(
		prompt.NewStandardInputParser(),
		func(info paste.Info) {
			a.cli.lastPasteInfo = &info
		},
	)
	noopCompleter := func(prompt.Document) []prompt.Suggest { return nil }
	return prompt.Input(
		"  > ",
		noopCompleter,
		prompt.OptionParser(pasteParser),
		prompt.OptionPrefixTextColor(prompt.Green),
		prompt.OptionInputTextColor(prompt.White),
	)
}

// applyPendingPasteInfo swaps the placeholder bracketed-paste left
// behind in the line back for the real captured content, then clears
// the paste-info pointer so the next chat-mode prompt doesn't see a
// stale notification from this coder iteration. No-op when no paste
// is pending or the placeholder isn't present in the line.
func (a *AgentMode) applyPendingPasteInfo(line string) string {
	if a.cli == nil || a.cli.lastPasteInfo == nil {
		return line
	}
	info := a.cli.lastPasteInfo
	a.cli.lastPasteInfo = nil
	if info.Placeholder != "" && strings.Contains(line, info.Placeholder) {
		return strings.Replace(line, info.Placeholder, info.Content, 1)
	}
	return line
}

// isMultilineTrigger reports whether the user typed one of the
// supported multiline-mode openers. Centralized so tests pin the
// exact set and a typo (e.g., "—" vs "---") doesn't silently work.
func isMultilineTrigger(s string) bool {
	return s == "---" || s == "```"
}

// runMultilineSession reads continuation lines from `reader` until
// the multilineBuf reports the session as complete (the user typed
// the matching delimiter). The trigger line itself is fed in first
// so the buffer captures the opener. Returns the full assembled
// text, trimmed.
func (a *AgentMode) runMultilineSession(trigger string, reader *bufio.Reader) (string, error) {
	a.multilineBuf.ProcessLine(trigger)
	fmt.Printf("\n  \033[90m📝 %s\033[0m\n", i18n.T("multiline.hint", a.multilineBuf.Delimiter()))
	for {
		fmt.Printf("  \033[90m... [%d] \033[0m", a.multilineBuf.LineCount()+1)
		nextLine, _ := reader.ReadString('\n')
		nextLine = strings.TrimRight(nextLine, "\r\n")
		complete, fullText := a.multilineBuf.ProcessLine(nextLine)
		if complete {
			return strings.TrimSpace(fullText), nil
		}
	}
}

// drainStdinToQueue moves any pending stdin lines into the message queue.
// Returns the first message if any, for immediate injection into conversation.
func (a *AgentMode) drainStdinToQueue() string {
	var first string
	for {
		select {
		case line := <-a.stdinLines:
			if line == "" {
				continue // skip empty lines (bare Enter presses)
			}
			if first == "" {
				first = line
			} else {
				a.cli.messageQueueMu.Lock()
				a.cli.messageQueue = append(a.cli.messageQueue, line)
				a.cli.messageQueueMu.Unlock()
			}
		default:
			return first
		}
	}
}

// CommandBlock and the other aliases below re-export the agent
// package types so legacy callers can continue to import them from
// cli without following the refactor chain.
type (
	CommandBlock       = agent.CommandBlock
	CommandOutput      = agent.CommandOutput
	CommandContextInfo = agent.CommandContextInfo
	SourceType         = agent.SourceType
)

// Constantes re-exportadas
const (
	SourceTypeUserInput     = agent.SourceTypeUserInput
	SourceTypeFile          = agent.SourceTypeFile
	SourceTypeCommandOutput = agent.SourceTypeCommandOutput
)

// sttyPath is the resolved absolute path to the stty binary (resolved once at
// init time via exec.LookPath to avoid PATH-based injection).
var sttyPath = func() string {
	if p, err := exec.LookPath("stty"); err == nil {
		return p
	}
	return "stty" // fallback for systems where LookPath fails
}()

// NewAgentMode cria uma nova instância do modo agente
func NewAgentMode(cli *ChatCLI, logger *zap.Logger) *AgentMode {
	a := &AgentMode{
		cli:            cli,
		logger:         logger,
		executor:       agent.NewCommandExecutor(logger),
		validator:      agent.NewCommandValidator(logger),
		contextManager: agent.NewContextManager(logger),
		taskTracker:    agent.NewTaskTracker(logger),
		turnTimer:      metrics.NewTimer(),
	}
	a.executeCommandsFunc = a.executeCommandsWithOutput
	return a
}

// getInput obtém entrada do usuário de forma segura.
// Uses the centralized stdin reader when active, falls back to direct read.
func (a *AgentMode) getInput(promptStr string) string {
	if runtime.GOOS != "windows" {
		cmd := exec.Command(sttyPath, "sane")
		cmd.Stdin = os.Stdin
		_ = cmd.Run()
	}

	// Enable bracketed paste mode for paste detection
	if runtime.GOOS != "windows" || os.Getenv("WT_SESSION") != "" {
		_, _ = os.Stdout.WriteString("\x1b[?2004h")
		defer func() { _, _ = os.Stdout.WriteString("\x1b[?2004l") }()
	}

	fmt.Print(promptStr)

	// Use centralized stdin reader (paste detection already handled there)
	return a.readLine()
}

// clientAndCtxForTurn resolves the LLM client and context for a single ReAct
// turn, honoring any skill model/effort hints captured at Run() start.
//
// Delegates model resolution to ChatCLI.resolveSkillClient so chat mode and
// agent mode share the same logic: API-cached lookup → catalog → family
// heuristic → optimistic user-provider attempt → graceful fallback with a
// user-visible message when the hint's target provider is unavailable.
//
// The effort hint is attached to ctx so the provider's SendPrompt can opt
// into extended thinking / reasoning_effort for this single call.
//
// Neither hint mutates a.cli.Client / a.cli.Provider / a.cli.Model — the
// user's active choices are preserved across turns.
func (a *AgentMode) clientAndCtxForTurn(ctx context.Context) (llmclient.LLMClient, context.Context) {
	turnClient := a.cli.Client
	if a.skillModelHint != "" {
		resolution := a.cli.resolveSkillClient(a.skillModelHint)
		turnClient = resolution.Client
		// Log once per turn — the ReAct loop can spin many times and we
		// don't want to spam. agent_mode.go only calls this helper from
		// the turn loop, so a single log per turn is acceptable.
		if resolution.Changed {
			a.logger.Debug("agent turn: skill model hint honored",
				zap.String("note", resolution.Note),
				zap.String("to_provider", resolution.Provider),
				zap.String("to_model", resolution.Model))
		}
	}
	// Honor /thinking session override before falling back to the skill
	// effort hint. EffortUnset inside an active override means "thinking
	// explicitly off" → skip both branches so the provider sends no hint.
	if eff, overridden := a.cli.applyThinkingOverride(a.skillEffortHint); overridden {
		if eff != llmclient.EffortUnset {
			ctx = llmclient.WithEffortHint(ctx, eff)
		}
	} else if a.skillEffortHint != llmclient.EffortUnset {
		ctx = llmclient.WithEffortHint(ctx, a.skillEffortHint)
	}
	return turnClient, ctx
}

// Run inicia o modo agente com uma consulta do usuário, utilizando um loop de Raciocínio-Ação (ReAct).
// Agora aceita systemPromptOverride para definir personas específicas (ex: Coder).
func (a *AgentMode) Run(ctx context.Context, query string, additionalContext string, systemPromptOverride string) error {
	// Reentrancy guard. AgentMode is not safe to Run concurrently on the
	// same instance — see runInflight comment in the struct. We CAS so
	// that any caller stepping on an in-flight Run gets a clean error
	// instead of corrupting shared state (taskTracker, history, TUI).
	if !a.runInflight.CompareAndSwap(false, true) {
		return fmt.Errorf("agent: another Run is already in flight on this AgentMode instance")
	}
	defer a.runInflight.Store(false)

	// Item 8: defeat typeahead-in-the-dark. The user may have just
	// pressed Enter on /coder or /agent — go-prompt's teardown can
	// leave the controlling TTY in raw mode (no echo, ICRNL off),
	// meaning any character typed during the spinner doesn't appear
	// on screen even though the kernel IS capturing it. Restoring
	// cooked terminal state up front ensures the user SEES what they
	// type while the LLM streams.
	coder.RestoreCookedMode()

	// --- 1. CONFIGURAÇÃO E PREPARAÇÃO DO AGENTE ---
	maxTurns := AgentMaxTurns()

	// Load the seven-pattern quality config eagerly so HyDE retrieval
	// (built BEFORE initMultiAgent) sees the right toggles. The
	// dispatcher pipeline wiring inside initMultiAgent re-reads the
	// same env, so the two views stay consistent.
	a.qualityConfig = quality.LoadFromEnv()
	// Apply session-level /refine and /verify overrides on top of env.
	if a.cli.qualityOverrides.Refine != nil {
		a.qualityConfig.Refine.Enabled = *a.cli.qualityOverrides.Refine
	}
	if a.cli.qualityOverrides.Verify != nil {
		a.qualityConfig.Verify.Enabled = *a.cli.qualityOverrides.Verify
	}

	// Save checkpoint before agent starts (for rewind support)
	a.cli.saveCheckpoint()

	a.logger.Info("Modo Agente iniciado", zap.Int("max_turns_limit", maxTurns))

	isCoder := (systemPromptOverride == CoderSystemPrompt)
	hasActivePersona := a.cli.personaHandler != nil && a.cli.personaHandler.GetManager().HasActiveAgent()

	// SYSTEM PROMPT COMPOSITION — structured for provider KV cache reuse.
	//
	// The agent system prompt is composed from four semantic blocks that
	// are built here and surfaced both as:
	//   • a flat string (systemInstruction) — used by providers without
	//     native cache_control (OpenAI, Gemini, Ollama, …) and by all the
	//     legacy accounting code that measures history by `len(Content)`.
	//   • structured SystemParts []ContentBlock — Anthropic consumes these
	//     directly, stamping cache_control:ephemeral on each boundary so
	//     identical prefixes are served as cache reads on subsequent turns.
	//
	// Block layout (stable → volatile):
	//   1. Core behavior (persona + format rules / CoderSystemPrompt /
	//      default agent prompt + language hint). Virtually never changes
	//      during a session → best cache candidate.
	//   2. Tools context (plugin descriptions) + session workspace hint.
	//      Changes only when plugins come/go or MCP shadows shift.
	//   3. Workspace context (SOUL.md/MEMORY.md, HyDE retrieval, dynamic
	//      context) — varies between runs but stable within one Run().
	//   4. Skills injection (auto-activated + manual) + Orchestrator
	//      catalog. Added further down after auto-activation is decided.
	var coreText string
	if hasActivePersona {
		personaPrompt := a.cli.personaHandler.GetManager().GetSystemPrompt()
		activeAgent := a.cli.personaHandler.GetManager().GetActiveAgent()
		if isCoder {
			coreText = personaPrompt + "\n\n" + CoderFormatInstructions
			a.logger.Info("Usando persona ativa + modo coder", zap.String("agent", activeAgent.Name))
		} else {
			coreText = personaPrompt + "\n\n" + AgentFormatInstructions
			a.logger.Info("Usando persona ativa + modo agent", zap.String("agent", activeAgent.Name))
		}
	} else if isCoder {
		coreText = CoderSystemPrompt
	} else {
		osName := runtime.GOOS
		shellName := utils.GetUserShell()
		currentDir, _ := os.Getwd()
		coreText = i18n.T("agent.system_prompt.default.base", osName, shellName, currentDir)
	}
	coreText += "\n\n" + i18n.T("ai.response_language")

	a.isCoderMode = isCoder
	a.isOneShot = false

	// Block 3 — workspace / retrieval context. Built only when we actually
	// have a context builder; empty string means "skip this block".
	var workspaceText string
	if a.cli.contextBuilder != nil {
		var hints []string
		hintWindow := 3
		if len(a.cli.history) < hintWindow {
			hintWindow = len(a.cli.history)
		}
		if hintWindow > 0 {
			var recentTexts []string
			for _, msg := range a.cli.history[len(a.cli.history)-hintWindow:] {
				recentTexts = append(recentTexts, msg.Content)
			}
			hints = memory.ExtractKeywords(recentTexts)
		}
		var wsCtx string
		if a.qualityConfig.HyDE.Enabled && a.qualityConfig.Enabled {
			wsCtx = a.cli.hydeRetrieveContext(ctx, query, hints, a.qualityConfig)
		} else {
			wsCtx = a.cli.contextBuilder.BuildSystemPromptPrefixWithHints(hints)
		}
		if wsCtx != "" {
			workspaceText = wsCtx
		}
		if dynCtx := a.cli.contextBuilder.BuildDynamicContext(); dynCtx != "" {
			if workspaceText != "" {
				workspaceText += "\n\n"
			}
			workspaceText += dynCtx
		}
	}

	// Block 2 — tool descriptions (plugins) + session workspace hint.
	// Merged into one cacheable block since they're always emitted as a pair.
	toolsText := a.getToolContextString() + buildSessionWorkspaceHint()

	// Fase 5: Inject auto-activated skills (triggers + path globs) into the
	// agent-mode system prompt. Also honors a `/<skill-name>` manual
	// invocation that was staged right before Run() was called — that is
	// how `/coder` and `/agent` interplay with skill invocation.
	//
	// Only fires once at the start of the loop so we don't inflate cache
	// across tool iterations.
	//
	// Reset hints from any previous Run() so a second `/agent` call
	// without an active skill does not inherit the old model/effort.
	a.skillModelHint = ""
	a.skillEffortHint = llmclient.EffortUnset
	// Block 4 — skills (pinned + auto-activated + manual) and Orchestrator
	// catalog. Built last because it's the most volatile (changes per query)
	// and sits at the tail of the system prompt so earlier blocks stay
	// cacheable. Pinned skills go before auto-activated so they win on
	// model/effort ties via pickSkillModelAndEffort's "first non-empty wins"
	// rule.
	skillsText := a.buildAgentSkillBlocks(query, additionalContext)
	if a.cli.pendingManualSkill != nil {
		manual := a.cli.pendingManualSkill
		manualArgs := a.cli.pendingManualSkillArgs
		a.cli.pendingManualSkill = nil
		a.cli.pendingManualSkillArgs = ""
		if block := renderManualSkillBlock(manual, manualArgs); block != "" {
			if skillsText != "" {
				skillsText += "\n\n"
			}
			skillsText += block
		}
		if m := strings.TrimSpace(manual.Model); m != "" {
			a.skillModelHint = m
		}
		if e := strings.TrimSpace(manual.Effort); e != "" {
			a.skillEffortHint = llmclient.NormalizeEffort(e)
		}
	}

	// If we captured a skill model hint, pre-resolve it once so the user
	// sees exactly what will run (or why their preference is being
	// ignored) before the agent loop starts burning turns.
	if a.skillModelHint != "" {
		a.cli.ensureModelCacheWarm()
		preview := a.cli.resolveSkillClient(a.skillModelHint)
		if preview.Changed {
			fmt.Printf("  %s\n", colorize(
				fmt.Sprintf("skill model hint: running agent on %s/%s",
					preview.Provider, preview.Model),
				ColorGray))
		} else if preview.UserMessage != "" {
			fmt.Printf("  %s\n", colorize("⚠ "+preview.UserMessage, ColorYellow))
		}
	}

	// Multi-Agent Orchestration: sempre ativo nos modos /agent e /coder.
	// A env CHATCLI_AGENT_PARALLEL_MODE pode desativar explicitamente (=false ou =0).
	var orchestratorText string
	if a.initMultiAgent() {
		orchestratorText = workers.OrchestratorSystemPrompt(a.agentRegistry.CatalogString())
	}

	// Banner curto com cheat sheet no modo /coder (apenas para o usuário humano)
	if isCoder && !a.coderBannerShown && isCoderBannerEnabled() {
		fmt.Println()
		fmt.Println(i18n.T("coder.quick_tip.header"))
		fmt.Println(i18n.T("coder.quick_tip.read"))
		fmt.Println(i18n.T("coder.quick_tip.search"))
		fmt.Println(i18n.T("coder.quick_tip.write"))
		fmt.Println(i18n.T("coder.quick_tip.exec"))
		fmt.Println()
		a.coderBannerShown = true
	}

	// Assemble the system message — flat string for providers without
	// cache_control (consumed via Message.Content) plus structured
	// SystemParts for Anthropic-style KV cache. Each block ends on a
	// cache boundary (ephemeral). The ordering matches the stability
	// heuristic described above.
	sysMsg := buildAgentSystemMessage(coreText, toolsText, workspaceText, skillsText, orchestratorText)

	// Inicializa ou atualiza o histórico com o System Prompt correto.
	//
	// Strategy: purge every stale `[ACTIVE MODE: …]` system message left
	// over from a previous /chat, /agent, or /coder turn — keeping any
	// non-mode system messages (e.g. /context attach blocks) untouched —
	// then prepend the current mode's sysMsg. This is the same filter
	// used by the chat pipeline; centralizing it in mode_transition.go
	// means a future change to the marker syntax has exactly one site
	// to edit.
	currentModeName := ModeAgent
	if isCoder {
		currentModeName = ModeCoder
	}
	a.cli.history = purgeStaleModeSystems(a.cli.history, currentModeName)

	if len(a.cli.history) == 0 {
		a.cli.history = append(a.cli.history, sysMsg)
	} else {
		// Replace any surviving system message of the CURRENT mode with
		// the freshly-built sysMsg (workspace/skills/orchestrator blocks
		// may have changed across turns). Otherwise prepend.
		foundSystem := false
		for i, msg := range a.cli.history {
			if msg.Role == "system" && modeOfSystemMessage(msg) == currentModeName {
				a.cli.history[i] = sysMsg
				foundSystem = true
				break
			}
		}
		if !foundSystem {
			a.cli.history = append([]models.Message{sysMsg}, a.cli.history...)
		}
	}

	currentQuery := query
	if additionalContext != "" {
		currentQuery += "\n\nContexto Adicional:\n" + additionalContext
	}

	// Inject K8s watcher context if active
	if a.cli.WatcherContextFunc != nil {
		if k8sCtx := a.cli.WatcherContextFunc(); k8sCtx != "" {
			currentQuery = k8sCtx + "\n\n" + currentQuery
		}
	}

	a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: currentQuery})

	// Phase 2 (#2): Plan-and-Solve / ReWOO. When the quality config asks
	// for it (mode=always, mode=auto + high complexity, or the user-set
	// pendingPlanFirst flag from /plan), synthesize a structured plan
	// and execute it deterministically before handing the conversation
	// to the orchestrator. The plan execution report is injected as a
	// system message so the ReAct loop can finalize with full context.
	a.runPlanFirstIfApplicable(ctx, currentQuery)

	// Dry-run / preview: runPlanFirstIfApplicable rendered the plan and
	// asked us to stop. Don't enter the ReAct loop — the user wanted to
	// inspect the plan before committing to execution.
	if a.cli.planDryRunHandled {
		a.cli.planDryRunHandled = false
		return nil
	}

	// --- 2. O LOOP DE RACIOCÍNIO-AÇÃO (ReAct) ---
	err := a.processAIResponseAndAct(ctx, maxTurns)
	// Park is a successful suspension, not an error. The user is back at
	// the prompt; the scheduler will fire the resume in due time.
	if errors.Is(err, errAgentParkedRequested) {
		return nil
	}
	return err
}

// RunCoderOnce executa o modo coder de forma não-interativa (one-shot),
// mas mantendo o loop ReAct do AgentMode (com tool_calls/plugins).
func (cli *ChatCLI) RunCoderOnce(ctx context.Context, input string) error {
	cli.setExecutionProfile(ProfileCoder)
	defer cli.setExecutionProfile(ProfileNormal)

	var query string
	if strings.HasPrefix(input, "/coder ") {
		query = strings.TrimPrefix(input, "/coder ")
	} else if input == "/coder" {
		return fmt.Errorf("entrada inválida para o modo coder one-shot: %s", input)
	} else {
		return fmt.Errorf("entrada inválida para o modo coder one-shot: %s", input)
	}

	// Processar contextos especiais como @file, @git, etc.
	query, additionalContext := cli.processSpecialCommands(query)
	fullQuery := query
	if additionalContext != "" {
		fullQuery = query + "\n\nContexto adicional:\n" + additionalContext
	}

	// Assegurar que o modo agente está inicializado
	if cli.agentMode == nil {
		cli.agentMode = NewAgentMode(cli, cli.logger)
	}

	cli.agentMode.isCoderMode = true
	cli.agentMode.isOneShot = true

	// Executa o AgentMode no "perfil coder" (system prompt override)
	// Isso mantém exatamente o fluxo atual do /coder interativo:
	// - timeline
	// - tool_call
	// - execução automática de plugins
	return cli.agentMode.Run(ctx, fullQuery, "", CoderSystemPrompt)
}

// RunOnce executa modo agente one-shot
func (a *AgentMode) RunOnce(ctx context.Context, query string, autoExecute bool) error {
	systemInstruction := i18n.T("agent.system_prompt.oneshot") + "\n\n" + i18n.T("ai.response_language")

	// Inject K8s watcher context if active
	enrichedQuery := query
	if a.cli.WatcherContextFunc != nil {
		if k8sCtx := a.cli.WatcherContextFunc(); k8sCtx != "" {
			enrichedQuery = k8sCtx + "\n\n" + query
		}
	}

	a.cli.history = append(a.cli.history, models.Message{Role: "system", Content: systemInstruction})
	a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: enrichedQuery})

	a.cli.animation.ShowThinkingAnimation(a.cli.Client.GetModelName())

	aiResponse, err := a.cli.Client.SendPrompt(ctx, enrichedQuery, a.cli.history, 0)
	// Auto-retry on OAuth token expiration (401)
	if a.cli.refreshClientOnAuthError(err) {
		aiResponse, err = a.cli.Client.SendPrompt(ctx, enrichedQuery, a.cli.history, 0)
	}
	a.cli.animation.StopThinkingAnimation()
	if err != nil {
		return fmt.Errorf("erro ao obter resposta da IA: %w", err)
	}

	// Track cost for agent mode initial call
	if a.cli.costTracker != nil {
		usage := llmclient.GetUsageOrEstimate(a.cli.Client, len(enrichedQuery), len(aiResponse))
		a.cli.costTracker.RecordRealUsage(a.cli.Provider, a.cli.Model, usage)
	}

	commandBlocks := a.extractCommandBlocks(aiResponse)
	a.displayResponseWithoutCommands(aiResponse, commandBlocks)

	if len(commandBlocks) == 0 {
		fmt.Println(i18n.T("agent.oneshot.no_command"))
		return nil
	}

	if !autoExecute {
		fmt.Println(i18n.T("agent.oneshot.header"))
		fmt.Println("==============================================")
		fmt.Println(i18n.T("agent.oneshot.auto_exec_tip"))

		block := commandBlocks[0]
		fmt.Println(i18n.T("agent.oneshot.block_header", block.Description))
		fmt.Println(i18n.T("agent.oneshot.language", block.Language))
		for _, cmd := range block.Commands {
			fmt.Printf("    $ %s\n", cmd)
		}

		return nil
	}

	fmt.Println(i18n.T("agent.oneshot.header_auto_exec"))
	fmt.Println("===============================================")

	blockToExecute := commandBlocks[0]

	for _, cmd := range blockToExecute.Commands {
		if a.validator.IsDangerous(cmd) {
			errMsg := i18n.T("agent.oneshot.auto_exec_aborted", cmd)
			fmt.Printf("⚠️ %s\n", errMsg)
			return errors.New(errMsg)
		}
	}

	fmt.Println(i18n.T("agent.oneshot.auto_exec_running"))
	_, errorMsg := a.executeCommandsWithOutput(ctx, blockToExecute)

	if errorMsg != "" {
		finalError := i18n.T("agent.oneshot.error_with_output", errorMsg)
		return fmt.Errorf("%s", finalError)
	}

	return nil
}

// getToolContextString centraliza a geração do contexto de ferramentas.
func (a *AgentMode) getToolContextString() string {
	if a.cli.pluginManager == nil {
		return ""
	}

	// Sync MCP shadow state: hide built-ins overridden by connected MCP servers,
	// restore them automatically when servers disconnect.
	if a.cli.mcpManager != nil {
		a.cli.pluginManager.SetShadowedBuiltins(a.cli.mcpManager.GetShadowedBuiltins())
	} else {
		a.cli.pluginManager.SetShadowedBuiltins(nil)
	}

	plugins := a.cli.pluginManager.GetPlugins()
	if len(plugins) == 0 {
		return ""
	}

	var toolDescriptions []string
	coderCheatSheet := ""
	for _, plugin := range plugins {

		var b strings.Builder
		b.WriteString(fmt.Sprintf("- Ferramenta: %s\n", plugin.Name()))
		b.WriteString(fmt.Sprintf("  Descrição: %s\n", plugin.Description()))

		if plugin.Schema() != "" {
			// Decodifica o schema para um formato estruturado
			var schema struct {
				ArgsFormat  string `json:"argsFormat"`
				Subcommands []struct {
					Name        string   `json:"name"`
					Description string   `json:"description"`
					Examples    []string `json:"examples"`
					Flags       []struct {
						Name        string `json:"name"`
						Description string `json:"description"`
						Type        string `json:"type"`
						Default     string `json:"default"`
						Required    bool   `json:"required"`
					} `json:"flags"`
				} `json:"subcommands"`
			}

			if err := json.Unmarshal([]byte(plugin.Schema()), &schema); err == nil {
				if a.isCoderMode {
					// Modo /coder: contexto compacto para reduzir ruído
					if schema.ArgsFormat != "" {
						b.WriteString(fmt.Sprintf("  Formato args: %s\n", schema.ArgsFormat))
					}
					b.WriteString("  Subcomandos:\n")
					for _, sub := range schema.Subcommands {
						b.WriteString(fmt.Sprintf("    - %s: %s\n", sub.Name, sub.Description))
						var requiredFlags []string
						for _, flag := range sub.Flags {
							if flag.Required {
								requiredFlags = append(requiredFlags, fmt.Sprintf("%s (%s)", flag.Name, flag.Type))
							}
						}
						if len(requiredFlags) > 0 {
							b.WriteString("      Obrigatórios: " + strings.Join(requiredFlags, ", ") + "\n")
						}
						if len(sub.Examples) > 0 {
							b.WriteString(fmt.Sprintf("      Ex: %s\n", sub.Examples[0]))
						}
					}
				} else {
					// Modo /agent: contexto completo
					if schema.ArgsFormat != "" {
						b.WriteString(fmt.Sprintf("  Formato args: %s\n", schema.ArgsFormat))
					}
					b.WriteString("  Subcomandos Disponíveis:\n")
					for _, sub := range schema.Subcommands {
						b.WriteString(fmt.Sprintf("    - %s: %s\n", sub.Name, sub.Description))
						if len(sub.Flags) > 0 {
							b.WriteString("      Flags:\n")
							for _, flag := range sub.Flags {
								req := ""
								if flag.Required {
									req = " [obrigatório]"
								}
								flagDesc := fmt.Sprintf("        - %s (%s)%s: %s", flag.Name, flag.Type, req, flag.Description)
								if flag.Default != "" {
									flagDesc += fmt.Sprintf(" (padrão: %s)", flag.Default)
								}
								b.WriteString(flagDesc + "\n")
							}
						}
						if len(sub.Examples) > 0 {
							limit := 2
							if len(sub.Examples) < limit {
								limit = len(sub.Examples)
							}
							b.WriteString("      Exemplos:\n")
							for i := 0; i < limit; i++ {
								b.WriteString(fmt.Sprintf("        - %s\n", sub.Examples[i]))
							}
						}
					}
				}
			} else {
				// Fallback para o uso antigo se o JSON do schema for inválido
				b.WriteString(fmt.Sprintf("  Uso: %s\n", plugin.Usage()))
			}
		} else {
			// Fallback para plugins que não têm o schema
			b.WriteString(fmt.Sprintf("  Uso: %s\n", plugin.Usage()))
		}

		toolDescriptions = append(toolDescriptions, b.String())
	}

	if a.isCoderMode {
		coderCheatSheet = "Cheat sheet (@coder):\n" +
			"- read: {\"cmd\":\"read\",\"args\":{\"file\":\"main.go\"}}\n" +
			"- search: {\"cmd\":\"search\",\"args\":{\"term\":\"Login\",\"dir\":\".\"}}\n" +
			"- write: {\"cmd\":\"write\",\"args\":{\"file\":\"x.go\",\"encoding\":\"base64\",\"content\":\"...\"}}\n" +
			"- exec: {\"cmd\":\"exec\",\"args\":{\"cmd\":\"mkdir -p testeapi\"}}\n\n"
	}

	// Include MCP tools from connected servers (deferred schemas — only name+description)
	// Full parameter schemas are fetched on-demand when the tool is invoked.
	if a.cli.mcpManager != nil {
		mcpTools := a.cli.mcpManager.GetToolsSummary()
		if len(mcpTools) > 0 {
			toolDescriptions = append(toolDescriptions, buildMCPToolsSection(mcpTools, a.isCoderMode))
		} else {
			// MCP is configured but no tools are usable right now: either a
			// background launch is still in progress or every server failed
			// to start. Tell the model explicitly so it does not fabricate
			// `mcp_*` calls and instead falls back to the listed tools.
			statuses := a.cli.mcpManager.GetServerStatus()
			if note := buildMCPEmptyNote(statuses); note != "" {
				toolDescriptions = append(toolDescriptions, note)
			}
		}
	}

	toolContext := "\n\n" + i18n.T("agent.system_prompt.tools_header") + "\n" + coderCheatSheet + strings.Join(toolDescriptions, "\n") + "\n\n" + i18n.T("agent.system_prompt.tools_instruction")
	if a.isCoderMode {
		toolContext += "\nDicas rápidas (@coder):\n" +
			"- Use args JSON sempre que possível: {\"cmd\":\"read\",\"args\":{\"file\":\"main.go\"}}\n" +
			"- Subcomando obrigatório: use \"cmd\" ou \"argv\".\n" +
			"- Para exec, use \"cmd\" (ou \"command\") dentro de args.\n"
	}
	return toolContext
}

// processAIResponseAndAct is the ReAct main loop. It is deliberately
// large — it interleaves stdin draining, LLM streaming, tool parsing,
// policy enforcement, compaction, and UI rendering — and a targeted
// refactor is scoped as its own effort outside the seven-pattern PR.
//
//nolint:gocyclo // legacy main loop; split tracked separately.
func (a *AgentMode) processAIResponseAndAct(ctx context.Context, maxTurns int) error {
	// Start centralized stdin reader for type-ahead queue support
	a.startStdinReader()
	defer a.stopStdinReader()

	renderer := agent.NewUIRenderer(a.logger)

	// Helper para construir o histórico com a "âncora" (System Prompt reforçado por turno).
	//
	// The anchor is deliberately verbose: tool results can be long enough
	// to push the primary system instructions out of the model's
	// attention window, especially for smaller / older models. Repeating
	// the operational rules every turn meaningfully improves format
	// compliance (tool_call batching, base64 writes, no loose code
	// blocks in /coder) at the cost of ~150 tokens/turn — a trade we
	// accept to protect quality across the full provider/model matrix.
	buildTurnHistoryWithAnchor := func() []models.Message {
		h := make([]models.Message, 0, len(a.cli.history)+1)
		h = append(h, a.cli.history...)

		var anchor string
		if a.isCoderMode {
			anchor = "REMINDER (/CODER MODE): You MUST respond with a short <reasoning> (2-6 lines) then emit one or more <tool_call name=\"@coder\" args=\"...\" />. " +
				"CRITICAL: Emit ALL independent tool_calls in a SINGLE response. Do NOT split independent reads/searches/writes into separate turns. " +
				"If you need to read 3 files, emit 3 tool_calls NOW, not one per turn. Use <agent_call> for 3+ independent tasks when available. " +
				"Do NOT use code blocks (```). For write/patch: base64 encoding and single-line args are MANDATORY."
		} else {
			anchor = "REMINDER (/AGENT MODE): You can use tools via <tool_call name=\"@tool\" args=\"...\" /> when appropriate. " +
				"CRITICAL: Emit ALL independent operations in a SINGLE response. Do NOT waste turns on things that could run in parallel. " +
				"For shell commands, use ```execute:<type>``` blocks (shell/git/docker/kubectl...). " +
				"Avoid destructive commands without clear warnings and alternatives."
		}

		h = append(h, models.Message{Role: "system", Content: anchor})
		return h
	}

	// Helper para verificar tags de raciocínio
	hasReasoningTag := func(s string) bool {
		ls := strings.ToLower(s)
		return strings.Contains(ls, "<reasoning>") && strings.Contains(ls, "</reasoning>")
	}

	// Helper local: renderizar um card com markdown usando o renderer do cli (glamour).
	renderMDCard := func(icon, title, md, color string) {
		md = strings.TrimSpace(md)
		if md == "" {
			return
		}
		rendered := a.cli.renderMarkdown(md) // retorna ANSI
		renderer.RenderMarkdownTimelineEvent(icon, title, rendered, color)
	}

	// Context recovery state for the session
	contextRecovery := agent.NewContextRecovery(agent.DefaultContextRecoveryConfig(), a.logger)
	currentMaxTokens := 0           // 0 = use provider default
	providerMaxTokensCap := 128_000 // conservative default; providers may support more

	// Stagnation detector — breaks out of the ReAct loop when the model
	// re-emits the SAME batch of tool_calls for N consecutive turns, which
	// is the "reflection loop" failure mode that makes trivial queries
	// burn tens of thousands of tokens. Gated by CHATCLI_AGENT_EARLY_EXIT.
	var stagnation *stagnationTracker
	if earlyExitEnabled() {
		stagnation = newStagnationTracker()
	}

	// --- LOOP PRINCIPAL DO AGENTE (ReAct) ---
	for turn := 0; turn < maxTurns; turn++ {
		// Verificar cancelamento pelo usuário (Ctrl+C)
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Check for type-ahead messages from user (works in both /agent and
		// /coder modes). Lines typed while the LLM was streaming or while a
		// tool was running get drained into the conversation as a fresh user
		// instruction on the next turn boundary. The previous behavior gated
		// this behind !a.isCoderMode, which meant /coder users had no way to
		// add a follow-up without waiting for the agent to finish — and any
		// keystrokes they emitted accidentally accumulated in the kernel TTY
		// buffer waiting to be consumed at the worst possible moment (the
		// next security prompt). The input guard (Fase 1.1) is what makes it
		// safe to enable this in /coder: typeahead caught BEFORE a security
		// prompt is now discarded, so the only thing reaching the queue is
		// input the user typed clearly between LLM turns.
		if userMsg := a.drainStdinToQueue(); userMsg != "" {
			label := i18n.T("agent.queue.new_user_instruction")
			fmt.Printf("\n  %s\n\n", renderer.Colorize("📨 "+label, agent.ColorCyan))
			a.cli.history = append(a.cli.history, models.Message{
				Role:    "user",
				Content: userMsg,
			})
		}

		// Microcompact (Level 0): cheap, progressive compaction of OLD tool
		// results in history. Pure Go, no LLM call. Often keeps us below
		// budget so the expensive summarization path is never triggered.
		mcCfg := agent.DefaultMicrocompactConfig()
		if h, report := agent.ApplyMicrocompact(a.cli.history, turn, mcCfg, a.logger); report != nil && (report.Truncated > 0 || report.Summarized > 0) {
			a.cli.history = h
			fmt.Printf("\r\033[K  %s %s\n",
				renderer.Colorize("🗜", agent.ColorGray),
				renderer.Colorize(
					i18n.T("agent.microcompact.applied",
						report.Truncated, report.Summarized, FormatPayloadSize(int(report.CharsSaved))),
					agent.ColorGray))
		}

		// Compact history if over budget (before building turn history)
		cfg := DefaultCompactConfig(a.cli.Provider, a.cli.Model)
		cfg.BudgetRatio = 0.60 // tighter budget — tool outputs are large
		cfg.MinKeepRecent = 8  // ~4 tool call cycles

		// Pre-flight: measure the current history and react BEFORE the
		// request goes out. Two paths:
		//   (a) User has CHATCLI_MAX_PAYLOAD set and we're above 85%
		//       of it → force aggressive compaction this turn.
		//   (b) No explicit cap but history is already > 2.5 MB → emit a
		//       one-shot hint about the env var. Corporate proxies usually
		//       cap at 5 MB and the error they return is obscure (403/EOF),
		//       so flagging this early saves the user a painful surprise.
		totalHistoryChars := 0
		for _, m := range a.cli.history {
			totalHistoryChars += len(m.Content)
		}
		if cfg.MaxPayloadBytes > 0 && totalHistoryChars > int(float64(cfg.MaxPayloadBytes)*0.85) {
			cfg.BudgetRatio = 0.40 // force harder compaction
			a.logger.Warn("Pre-flight: history near payload cap, forcing aggressive compact",
				zap.Int("total_chars", totalHistoryChars),
				zap.Int("cap_bytes", cfg.MaxPayloadBytes))
			fmt.Printf("\r\033[K  %s %s\n",
				renderer.Colorize("ℹ", agent.ColorGray),
				renderer.Colorize(
					i18n.T("agent.preflight.near_cap",
						FormatPayloadSize(totalHistoryChars),
						(totalHistoryChars*100)/cfg.MaxPayloadBytes,
						FormatPayloadSize(cfg.MaxPayloadBytes)),
					agent.ColorGray))
		} else if cfg.MaxPayloadBytes == 0 && totalHistoryChars > 2_500_000 && !a.proxyPayloadWarned {
			a.proxyPayloadWarned = true
			a.logger.Warn("History exceeds 2.5 MB, no payload cap set — proxy 413/403 possible",
				zap.Int("total_chars", totalHistoryChars))
			fmt.Printf("\r\033[K  %s %s\n",
				renderer.Colorize("ℹ", agent.ColorYellow),
				renderer.Colorize(
					i18n.T("agent.preflight.warn_no_cap", FormatPayloadSize(totalHistoryChars)),
					agent.ColorYellow))
		}
		if a.cli.historyCompactor.NeedsCompaction(a.cli.history, cfg) {
			// Emit live status during compaction so the terminal is never
			// silent. Without this, Level 2 (LLM summarization) can block
			// for 30-90s with zero feedback — users assume a freeze.
			a.cli.historyCompactor.SetStatusCallback(func(stage CompactStage, msg string) {
				fmt.Printf("\r\033[K  %s %s\n",
					renderer.Colorize("│", agent.ColorCyan),
					renderer.Colorize(msg, agent.ColorGray))
			})
			compacted, compactErr := a.cli.historyCompactor.Compact(ctx, a.cli.history, a.cli.Client, cfg)
			a.cli.historyCompactor.SetStatusCallback(nil)
			if compactErr == nil {
				a.cli.history = compacted
			} else if errors.Is(compactErr, context.Canceled) {
				return compactErr
			}
		}

		a.logger.Debug("Iniciando turno do agente", zap.Int("turn", turn+1), zap.Int("max_turns", maxTurns))

		// Reset per-turn counters
		turnAgents := 0
		turnToolCalls := 0

		// Inicia o timer do turno (substitui a animação de "Pensando...").
		// Item 8: indicator reflete TANTO as linhas já drenadas para a
		// messageQueue QUANTO as linhas em trânsito no channel
		// stdinLines (Enter foi pressionado mas o turn atual ainda não
		// fechou para drenar). Sem essa soma, o usuário pressiona Enter
		// e nada muda visualmente no spinner — só vê (1 na fila) na
		// próxima iteração do turn.
		modelName := a.cli.Client.GetModelName()
		a.turnTimer.Start(ctx, func(d time.Duration) {
			msg := "Processando..."
			a.cli.messageQueueMu.Lock()
			queued := len(a.cli.messageQueue)
			a.cli.messageQueueMu.Unlock()
			if a.stdinLines != nil {
				queued += len(a.stdinLines)
			}
			if queued > 0 {
				msg = "Processando... " + i18n.T("agent.queue.indicator", queued)
			}
			fmt.Print(metrics.FormatTimerStatus(d, modelName, msg))
		})

		turnHistory := buildTurnHistoryWithAnchor()

		// Validate tool result pairing and enforce budget before sending to API
		turnHistory, pairingReport := agent.EnsureToolResultPairing(turnHistory, a.logger)
		if pairingReport.HasRepairs() {
			a.logger.Info("Tool result pairing repaired before API call",
				zap.Int("synthetic_results", pairingReport.SyntheticResultsInjected),
				zap.Int("orphans_removed", pairingReport.OrphanResultsRemoved))
		}
		turnHistory, _ = agent.EnforceToolResultBudget(turnHistory, a.logger)

		// Resolve per-turn client + effort hint from any active skill
		// hints. Model swap is transparent; effort flows via ctx so the
		// provider's SendPrompt can enable extended thinking / reasoning.
		turnClient, turnCtx := a.clientAndCtxForTurn(ctx)

		// Detect native function calling support
		var nativeToolCalls []models.ToolCall
		toolAwareClient, canUseNativeTools := llmclient.AsToolAware(turnClient)
		if canUseNativeTools && !toolAwareClient.SupportsNativeTools() {
			canUseNativeTools = false
		}

		// Get tool definitions for native mode.
		// In coder mode: coder tools + plugin tools (websearch, webfetch)
		// In agent mode: plugin tools only (websearch, webfetch)
		// MCP tools from any connected server are exposed in both modes so
		// the model can call them via the provider's native tool API rather
		// than relying on text-only XML dispatch.
		var nativeToolDefs []models.ToolDefinition
		if canUseNativeTools {
			if a.isCoderMode {
				nativeToolDefs = workers.CoderToolDefinitions(nil)
			}
			nativeToolDefs = append(nativeToolDefs, workers.PluginToolDefinitions()...)
			if a.cli.mcpManager != nil && a.cli.mcpManager.ToolCount() > 0 {
				nativeToolDefs = append(nativeToolDefs, a.cli.mcpManager.GetTools()...)
			}
		}

		// Chamada à LLM (native tools or text)
		var aiResponse string
		var err error
		var llmResp *models.LLMResponse

		if canUseNativeTools && len(nativeToolDefs) > 0 {
			llmResp, err = toolAwareClient.SendPromptWithTools(turnCtx, "", turnHistory, nativeToolDefs, currentMaxTokens)
			if a.cli.refreshClientOnAuthError(err) {
				llmResp, err = toolAwareClient.SendPromptWithTools(turnCtx, "", turnHistory, nativeToolDefs, currentMaxTokens)
			}
			if err == nil && llmResp != nil {
				aiResponse = llmResp.Content
				nativeToolCalls = llmResp.ToolCalls
			}
		} else {
			aiResponse, err = turnClient.SendPrompt(turnCtx, "", turnHistory, currentMaxTokens)
			if a.cli.refreshClientOnAuthError(err) {
				aiResponse, err = turnClient.SendPrompt(turnCtx, "", turnHistory, currentMaxTokens)
			}
		}

		// Track cost for agent mode turn — prefer real API usage.
		// Attribute to whichever provider+model the resolver actually
		// served this turn (see clientAndCtxForTurn).
		if a.cli.costTracker != nil && err == nil {
			inputChars := 0
			for _, m := range turnHistory {
				inputChars += len(m.Content)
			}
			usage := llmclient.GetUsageOrEstimate(turnClient, inputChars, len(aiResponse))
			effProvider := a.cli.Provider
			effModel := a.cli.Model
			if a.skillModelHint != "" {
				resolution := a.cli.resolveSkillClient(a.skillModelHint)
				effProvider = resolution.Provider
				effModel = resolution.Model
			}
			a.cli.costTracker.RecordRealUsage(effProvider, effModel, usage)
		}

		// Para o timer e obtém a duração
		turnDuration := a.turnTimer.Stop()
		fmt.Print(metrics.ClearLine()) // Limpa a linha do timer
		fmt.Println()

		// Helper para exibir métricas ao final do turno (após execução)
		showTurnStats := func() {
			fmt.Println(metrics.FormatTurnInfo(turn+1, maxTurns, turnDuration, &metrics.TurnStats{
				TurnAgents:       turnAgents,
				TurnToolCalls:    turnToolCalls,
				SessionAgents:    a.agentsLaunched,
				SessionToolCalls: a.toolCallsExecd,
			}))
		}

		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}

			// Context overflow OR corporate-proxy payload rejection (413,
			// WAF 403, 431, or EOF-on-large-payload): try to recover by
			// aggressive compaction + retry. Proxy rejections are pernicious
			// — the user has no way to know why their perfectly-sized-for-
			// the-model request was rejected by a network middlebox.
			isCtxTooLong := agent.IsContextTooLongError(err)
			historyChars := 0
			for _, m := range a.cli.history {
				historyChars += len(m.Content)
			}
			isPayloadTooLarge := agent.IsLikelyPayloadProblem(err, historyChars)
			if (isCtxTooLong || isPayloadTooLarge) && contextRecovery.CanRecoverContextOverflow() {
				reason := i18n.T("agent.recovery.reason.ctx_too_long")
				if isPayloadTooLarge {
					switch {
					case agent.IsPayloadTooLargeError(err):
						reason = i18n.T("agent.recovery.reason.payload_413")
					case agent.IsProxyWAFRejection(err):
						reason = i18n.T("agent.recovery.reason.waf_403")
					default:
						reason = i18n.T("agent.recovery.reason.suspected_payload", FormatPayloadSize(historyChars))
					}
				}
				a.logger.Warn("Recoverable request failure — attempting recovery",
					zap.String("reason", reason),
					zap.Int("turn", turn+1), zap.Error(err))

				fmt.Printf("\r\033[K  %s %s\n",
					renderer.Colorize("⚠", agent.ColorYellow),
					renderer.Colorize(
						i18n.T("agent.recovery.retrying", reason),
						agent.ColorYellow))

				// For payload-too-large: also force the compactor to use a
				// hard byte cap on the next turn. Without a cap hint we would
				// just retry with the same size and fail again.
				if isPayloadTooLarge && os.Getenv("CHATCLI_MAX_PAYLOAD") == "" {
					// Educated guess: assume a 4 MB proxy cap as a sane default
					// when the user hasn't configured one. Err on the side of
					// caution — easier to set a higher value than to hit this
					// error repeatedly.
					_ = os.Setenv("CHATCLI_MAX_PAYLOAD", "4MB")
					fmt.Printf("  %s %s\n",
						renderer.Colorize("ℹ", agent.ColorGray),
						renderer.Colorize(
							i18n.T("agent.recovery.assumed_cap"),
							agent.ColorGray))
				}

				recoveredHistory, recovered := contextRecovery.RecoverContextOverflow(a.cli.history)
				if recovered {
					a.cli.history = recoveredHistory

					// Post-recovery hint for payload-related failures: without
					// this, the model tends to re-read the same huge file that
					// triggered the limit, looping through recovery until
					// MaxRecoveryAttempts is exhausted. A single user-role
					// instruction steers it toward surgical, line-ranged reads
					// that fit under the cap.
					if isPayloadTooLarge {
						a.cli.history = append(a.cli.history, models.Message{
							Role: "user",
							Content: "[SYSTEM NOTICE — PAYLOAD LIMIT HIT] A proxy/gateway rejected the previous request due to body size. History was compacted to recover. " +
								"Going forward: " +
								"(1) When reading files, prefer targeted reads with line ranges (e.g. sed -n '100,200p' file, or read_file with offset+limit) instead of reading entire files. " +
								"(2) Prefer grep/ripgrep with specific patterns over full-file reads. " +
								"(3) If you previously read a large file, its full content is persisted at the path shown in the tool-result preview — re-read specific ranges from that file rather than repeating the original read. " +
								"(4) Summarize findings incrementally rather than accumulating raw tool output.",
						})
						a.logger.Info("Injected payload-recovery hint into history")
					}
					continue // retry the turn with compacted history
				}
			}

			return fmt.Errorf("erro ao obter resposta da IA no turno %d: %w", turn+1, err)
		}

		// Max-output-tokens recovery: detect truncation and escalate
		stopReason := ""
		if llmResp != nil && llmResp.StopReason != "" {
			stopReason = llmResp.StopReason
		} else if src, ok := llmclient.AsStopReasonAware(a.cli.Client); ok {
			stopReason = src.LastStopReason()
		}
		if stopReason == "max_tokens" || stopReason == "length" {
			effectiveMax := currentMaxTokens
			if effectiveMax <= 0 {
				effectiveMax = 4096 // common default
			}
			if newMax, ok := contextRecovery.MaxTokensEscalation(effectiveMax, providerMaxTokensCap); ok {
				currentMaxTokens = newMax
				a.cli.history = append(a.cli.history, models.Message{
					Role:    "assistant",
					Content: aiResponse,
				})
				a.cli.history = append(a.cli.history, agent.ContinuationMessage())
				a.logger.Info("Max tokens hit — escalated and continuing",
					zap.Int("new_max_tokens", currentMaxTokens))
				continue // retry with higher limit
			}
		}

		// Persistir a resposta no histórico "real"
		if len(nativeToolCalls) > 0 {
			a.cli.history = append(a.cli.history, models.Message{
				Role:      "assistant",
				Content:   aiResponse,
				ToolCalls: nativeToolCalls,
			})
		} else {
			a.cli.history = append(a.cli.history, models.Message{Role: "assistant", Content: aiResponse})
		}

		// Parsear Tool Calls — native ou XML fallback
		var toolCalls []agent.ToolCall
		if len(nativeToolCalls) > 0 {
			// Convert native tool calls to agent.ToolCall format.
			// Plugin tools (web_search, web_fetch) map to their respective plugins.
			// Coder tools (read_file, write_file, etc.) map to @coder.
			for _, ntc := range nativeToolCalls {
				if pluginName, pluginArgs, isPlugin := workers.ResolveNativePluginTool(ntc.Name, ntc.Arguments); isPlugin {
					// Plugin tool: map to the plugin's CLI args format
					argsStr := strings.Join(pluginArgs, " ")
					toolCalls = append(toolCalls, agent.ToolCall{
						Name: pluginName,
						Args: argsStr,
						Raw:  argsStr,
					})
				} else {
					// Coder tool: map to @coder with JSON args
					subcmd, _ := workers.NativeToolNameToSubcmd(ntc.Name)
					argsJSON, _ := json.Marshal(map[string]interface{}{
						"cmd":  subcmd,
						"args": ntc.Arguments,
					})
					toolCalls = append(toolCalls, agent.ToolCall{
						Name: "@coder",
						Args: string(argsJSON),
						Raw:  string(argsJSON),
					})
				}
			}
		} else {
			var parseErr error
			toolCalls, parseErr = agent.ParseToolCalls(aiResponse)
			if parseErr != nil {
				a.logger.Warn("Falha ao parsear tool_calls", zap.Error(parseErr))
				toolCalls = nil
			}
		}

		// Stagnation check: fingerprint this turn's tool_call batch and
		// break if we've seen the SAME batch N turns in a row. The model
		// is almost certainly looping on the same information — no amount
		// of additional turns will converge it, and every extra turn
		// burns the full system prompt + tool definitions.
		//
		// The detector is a no-op when the fingerprint is empty (zero
		// tool_calls), so the existing "no tools = final answer / wait
		// for user" paths further down are reached normally.
		if stagnation != nil {
			fp := toolCallFingerprint(toolCalls)
			if stalled, repeats := stagnation.Observe(fp); stalled {
				a.logger.Warn("ReAct stagnation detected — breaking out of loop",
					zap.Int("repeated_turns", repeats),
					zap.Int("turn", turn+1))
				fmt.Printf("\r\033[K  %s %s\n",
					renderer.Colorize("⚠", agent.ColorYellow),
					renderer.Colorize(
						i18n.T("agent.early_exit.stagnation", repeats),
						agent.ColorYellow))
				// Acknowledge the partial assistant text so a later
				// manual continuation starts from a consistent state.
				a.cli.history = append(a.cli.history, models.Message{
					Role: "user",
					Content: "[stagnation detected] The same tool call was emitted repeatedly without new information. " +
						"Stopping the ReAct loop. If you need to continue, rephrase the request or provide more context.",
				})
				fmt.Println(metrics.FormatTurnInfo(turn+1, maxTurns, turnDuration, &metrics.TurnStats{
					TurnAgents:       turnAgents,
					TurnToolCalls:    turnToolCalls,
					SessionAgents:    a.agentsLaunched,
					SessionToolCalls: a.toolCallsExecd,
				}))
				return nil
			}
		}

		// Separar pensamento (texto antes do primeiro tool_call)
		thoughtText := strings.TrimSpace(aiResponse)
		if len(toolCalls) > 0 {
			firstRaw := toolCalls[0].Raw
			parts := strings.Split(aiResponse, firstRaw)
			thoughtText = strings.TrimSpace(parts[0])
		}

		// ==============
		// RENDERIZAÇÃO DE PENSAMENTO (Timeline)
		// ==============
		reasoning, _ := extractXMLTagContent(thoughtText, "reasoning")
		explanation, _ := extractXMLTagContent(thoughtText, "explanation")

		remaining := thoughtText
		remaining = stripXMLTagBlock(remaining, "reasoning")
		remaining = stripXMLTagBlock(remaining, "explanation")
		remaining = stripXMLTagBlock(remaining, "final_summary")
		remaining = stripXMLTagBlock(remaining, "plan")
		remaining = stripXMLTagBlock(remaining, "summary")
		remaining = stripXMLTagBlock(remaining, "action")
		remaining = stripXMLTagBlock(remaining, "action_type")
		remaining = stripXMLTagBlock(remaining, "command")
		remaining = stripXMLTagBlock(remaining, "step")
		remaining = stripAgentCallTags(remaining)
		remaining = stripToolCallTags(remaining)
		remaining = strings.TrimSpace(removeXMLTags(remaining))

		// Style flags now sourced from the renderer (env-driven), not
		// gated by isCoderMode — the CHATCLI_CODER_UI variable controls
		// both /coder and /agent surfaces after the cross-mode unification.
		isCompact := renderer.IsCompact()
		isMinimal := renderer.IsMinimal()

		if strings.TrimSpace(reasoning) != "" {
			switch {
			case isCompact:
				renderer.CompactMultiLine("●", "PLANO", reasoning, agent.ColorCyan, 5)
			case isMinimal:
				renderer.RenderTimelineEvent("🧭", "PLANO", compactText(reasoning, 3, 260), agent.ColorCyan)
			default:
				renderMDCard("🧠", i18n.T("agent.ui.reasoning_title"), reasoning, agent.ColorCyan)
			}
			// Integração de Task Tracking (somente no modo /coder)
			if a.isCoderMode {
				agent.IntegrateTaskTracking(a.taskTracker, reasoning, a.logger)
			}
		}
		if strings.TrimSpace(explanation) != "" {
			switch {
			case isCompact:
				renderer.CompactLine("◆", "NOTA", explanation, agent.ColorLime)
			case isMinimal:
				renderer.RenderTimelineEvent("📝", "NOTA", compactText(explanation, 2, 220), agent.ColorLime)
			default:
				renderMDCard("📌", "EXPLICAÇÃO", explanation, agent.ColorLime)
			}
		}
		// Helper para renderizar progresso atualizado do plano
		renderPlanProgress := func() {
			if !a.isCoderMode || a.taskTracker == nil || a.taskTracker.GetPlan() == nil {
				return
			}
			progress := a.taskTracker.FormatProgress()
			if strings.TrimSpace(progress) == "" {
				return
			}
			switch {
			case isCompact:
				renderer.CompactMultiLine("◇", "STATUS", progress, agent.ColorLime, 4)
			case isMinimal:
				renderer.RenderTimelineEvent("🧩", "STATUS", compactText(progress, 2, 220), agent.ColorLime)
			default:
				renderMDCard("🧩", "PLANO DE AÇÃO", progress, agent.ColorLime)
			}
		}

		// Renderizar progresso inicial das tarefas (somente no modo /coder)
		renderPlanProgress()
		if strings.TrimSpace(remaining) != "" {
			switch {
			case isCompact:
				// Compact mode now also runs the assistant's answer
				// through glamour before printing, matching the full-
				// mode card. Without this, markdown tables and **bold**
				// arrived raw in the timeline (the user could see the
				// pipes and asterisks as literal characters) and the
				// compact "answer" line looked broken compared to the
				// full-mode card. Trim the glamour bookend newlines so
				// CompactAssistantText doesn't reserve a leading ◆ row
				// for an empty line.
				renderedMD := strings.Trim(a.cli.renderMarkdown(remaining), "\n\r")
				renderer.CompactAssistantText(renderedMD)
			case isMinimal:
				renderer.RenderTimelineEvent("💬", "RESUMO", compactText(remaining, 2, 220), agent.ColorGray)
			default:
				renderMDCard("💬", "RESPOSTA", remaining, agent.ColorGray)
			}
		}

		// =========================
		// VALIDAÇÕES DO /CODER
		// =========================
		if a.isCoderMode {
			if len(toolCalls) > 0 {
				// Require <reasoning> before acting
				if !hasReasoningTag(thoughtText) {
					a.cli.history = append(a.cli.history, models.Message{
						Role: "user",
						Content: "FORMAT ERROR: In /coder mode, you MUST write a <reasoning> block (2-6 lines with task list) BEFORE any <tool_call>. " +
							"Rewrite your response starting with <reasoning>...</reasoning> then your <tool_call> tags.",
					})
					continue
				}

				// Validate that the tool exists (any registered plugin, MCP tool, or @coder)
				firstName := strings.TrimSpace(toolCalls[0].Name)
				isKnownTool := false
				if a.cli.pluginManager != nil {
					if _, found := a.cli.pluginManager.GetPlugin(firstName); found {
						isKnownTool = true
					}
				}
				if !isKnownTool && a.cli.mcpManager != nil && strings.HasPrefix(firstName, "mcp_") {
					mcpName := strings.TrimPrefix(firstName, "mcp_")
					if a.cli.mcpManager.IsMCPTool(mcpName) {
						isKnownTool = true
					}
				}
				if !isKnownTool {
					// Build list of available tools for the error message
					var availableTools []string
					if a.cli.pluginManager != nil {
						for _, p := range a.cli.pluginManager.GetPlugins() {
							availableTools = append(availableTools, p.Name())
						}
					}
					a.cli.history = append(a.cli.history, models.Message{
						Role: "user",
						Content: fmt.Sprintf("FORMAT ERROR: Tool %q not found. Available tools: %s. "+
							"Use <tool_call name=\"TOOL\" args='...' />",
							firstName, strings.Join(availableTools, ", ")),
					})
					continue
				}
			}

			// Prohibit loose code blocks in coder mode
			if len(toolCalls) == 0 {
				if strings.Contains(aiResponse, "```") || strings.Contains(aiResponse, "```execute:") || regexp.MustCompile(`(?m)^[$#]\s+`).MatchString(aiResponse) {
					a.cli.history = append(a.cli.history, models.Message{
						Role: "user",
						Content: "FORMAT ERROR: Code blocks and shell commands are NOT allowed in /coder mode. " +
							"You MUST use <reasoning> followed by <tool_call> tags. " +
							"For shell commands: <tool_call name=\"@coder\" args='{\"cmd\":\"exec\",\"args\":{\"cmd\":\"your command\"}}' />",
					})
					continue
				}
			}
		}

		// =========================================================
		// PRIORIDADE 0: DISPATCH AGENT_CALL(s) (MULTI-AGENT MODE)
		// =========================================================
		if a.parallelMode && a.agentDispatcher != nil {
			agentCalls, _ := workers.ParseAgentCalls(aiResponse)
			if len(agentCalls) > 0 {
				isCompactUI := renderer.IsCompact()
				isMinimalUI := renderer.IsMinimal()
				n := len(agentCalls)
				agentWord := "agent"
				if n > 1 {
					agentWord = "agents"
				}
				switch {
				case isCompactUI:
					renderer.CompactLine("●", "AGENTS", fmt.Sprintf("%d %s", n, agentWord), agent.ColorPurple)
				case isMinimalUI:
					renderer.RenderTimelineEvent("🚀", "AGENTS", fmt.Sprintf("%d %s dispatched", n, agentWord), agent.ColorPurple)
				default:
					renderer.RenderTimelineEvent("🚀", "MULTI-AGENT DISPATCH", fmt.Sprintf("Dispatching %d %s", n, agentWord), agent.ColorPurple)
				}

				if !isCompactUI {
					for i, ac := range agentCalls {
						renderer.RenderTimelineEvent("🤖", fmt.Sprintf("[%s] #%d", ac.Agent, i+1), truncateForUI(ac.Task, 120), agent.ColorCyan)
					}
				}

				// Dispatch with live progress feedback
				a.agentsLaunched += len(agentCalls)
				turnAgents += len(agentCalls)

				// Build progress state for live display
				agentSlots := make([]struct{ CallID, Agent, Task string }, n)
				for idx, ac := range agentCalls {
					agentSlots[idx] = struct{ CallID, Agent, Task string }{ac.ID, string(ac.Agent), ac.Task}
				}
				progressState := metrics.NewAgentProgressState(n, agentSlots)

				// Progress event channel - consumed by a goroutine that updates state
				progressCh := make(chan workers.AgentEvent, n*2)
				go func() {
					for evt := range progressCh {
						switch evt.Type {
						case workers.AgentEventStarted:
							progressState.MarkStarted(evt.CallID)
						case workers.AgentEventCompleted:
							progressState.MarkCompleted(evt.CallID, evt.Duration)
						case workers.AgentEventFailed:
							errMsg := ""
							if evt.Error != nil {
								errMsg = evt.Error.Error()
							}
							progressState.MarkFailed(evt.CallID, evt.Duration, errMsg)
						}
					}
				}()

				// Track how many lines the progress display uses for clearing.
				// Both displayFunc and onPause closures share prevLines — safe
				// because both execute under Timer.mu.
				prevLines := 0
				a.turnTimer.SetOnPause(func() {
					// Clear the entire multi-line progress display before a
					// security prompt takes over. Reset prevLines so Resume
					// starts fresh and won't try to clear prompt lines.
					if prevLines > 0 {
						fmt.Print(metrics.ClearLines(prevLines))
						fmt.Print(metrics.ClearLine())
						prevLines = 0
					}
				})
				a.turnTimer.Start(ctx, func(d time.Duration) {
					if prevLines > 0 {
						fmt.Print(metrics.ClearLines(prevLines))
					}
					output := metrics.FormatDispatchProgress(progressState, modelName)
					fmt.Print(output)
					prevLines = progressState.LineCount()
				})

				// Give the policy adapter access to the spinner and stdin
				// channel so it can pause/resume around interactive
				// security prompts and read input without orphaning goroutines.
				if a.policyAdapter != nil {
					a.policyAdapter.setSpinner(a.turnTimer)
					a.policyAdapter.setStdinCh(a.stdinLines)
				}
				agentResults := a.agentDispatcher.DispatchWithProgress(ctx, agentCalls, progressCh)
				a.turnTimer.Stop()
				// Clear the live progress display
				if prevLines > 0 {
					fmt.Print(metrics.ClearLines(prevLines))
				}
				fmt.Print(metrics.ClearLine())

				// Render results and count internal tool calls
				totalAgentToolCalls := 0
				totalParallelCalls := 0
				successCount := 0
				var totalDuration time.Duration
				for _, ar := range agentResults {
					tcCount := len(ar.ToolCalls)
					totalAgentToolCalls += tcCount
					totalParallelCalls += ar.ParallelCalls
					totalDuration += ar.Duration
					if ar.Error != nil {
						if isCompactUI {
							renderer.CompactToolDone(string(ar.Agent), ar.Duration.Round(time.Millisecond).String(), true)
						} else {
							renderer.RenderTimelineEvent("❌", fmt.Sprintf("[%s] FAILED", ar.Agent), ar.Error.Error(), agent.ColorYellow)
						}
					} else {
						successCount++
						if isCompactUI {
							renderer.CompactToolDone(fmt.Sprintf("%s(%d calls)", ar.Agent, tcCount), ar.Duration.Round(time.Millisecond).String(), false)
						} else {
							summary := truncateForUI(ar.Output, 200)
							parallelInfo := ""
							if ar.ParallelCalls > 1 {
								parallelInfo = fmt.Sprintf(", %d em paralelo", ar.ParallelCalls)
							}
							tcLabel := "tool calls"
							if tcCount == 1 {
								tcLabel = "tool call"
							}
							title := fmt.Sprintf("[%s] OK (%s, %d %s%s)", ar.Agent, ar.Duration.Round(time.Millisecond), tcCount, tcLabel, parallelInfo)
							renderer.RenderTimelineEvent("✅", title, summary, agent.ColorGreen)
						}
					}
				}
				a.toolCallsExecd += totalAgentToolCalls
				turnToolCalls += totalAgentToolCalls

				if !isCompactUI {
					// Resumo compacto do dispatch
					tcWord := "tool calls"
					if totalAgentToolCalls == 1 {
						tcWord = "tool call"
					}
					parallelSuffix := ""
					if totalParallelCalls > 1 {
						parallelSuffix = fmt.Sprintf(" | %d goroutines paralelas", totalParallelCalls)
					}
					renderer.RenderTimelineEvent("📊", "RESUMO",
						fmt.Sprintf("%d/%d %s concluidos | %d %s executadas%s | %s total",
							successCount, n, agentWord, totalAgentToolCalls, tcWord,
							parallelSuffix, totalDuration.Round(time.Millisecond)),
						agent.ColorGray)
				}

				// Inject results as feedback for the orchestrator
				feedback := workers.FormatResults(agentResults)
				a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: feedback})

				// If there are also tool_calls in the same response, skip them —
				// the orchestrator should use agent_calls OR tool_calls, not both in the same turn.
				if len(toolCalls) > 0 {
					a.logger.Info("Skipping tool_calls because agent_calls were dispatched in this turn")
				}
				showTurnStats()
				continue
			}
		}

		// =========================================================
		// PRIORIDADE 1: EXECUTAR TOOL_CALL(s) EM LOTE (BATCH)
		// =========================================================
		if len(toolCalls) > 0 {
			// isCompact / isMinimal already resolved at the top of this
			// for-iteration via renderer.IsCompact()/IsMinimal() — reuse
			// them instead of re-reading the env, so a future caller can
			// override the style for a single iteration if needed.
			var batchOutputBuilder strings.Builder
			var batchHasError bool
			successCount := 0
			totalActions := len(toolCalls)

			// turnToolResults captures the per-tool structured outcome
			// alongside the legacy batchOutputBuilder concatenation. Today
			// only telemetry consumes it; Fase 3 (parallel orchestration)
			// and Fase 5 (provider-aware tool_result block emission) will
			// route this slice through their respective layers. Keeping it
			// populated unconditionally now means future phases can flip on
			// without a second pass through this critical loop.
			turnToolResults := make([]agent.ToolResult, 0, totalActions)

			// Helper: render error message respecting compact mode
			renderError := func(msg string) {
				if isCompact {
					renderer.CompactError(msg)
				} else if isMinimal {
					renderer.RenderToolResultMinimal(msg, true)
				} else {
					renderer.RenderToolResult(msg, true)
				}
			}

			// 1. Renderiza cabeçalho do lote se houver mais de 1 ação
			if totalActions > 1 {
				switch {
				case isCompact:
					// Compact mode: no batch header, just tool lines.
				case isMinimal:
					renderer.RenderTimelineEvent("📦", "LOTE", fmt.Sprintf("%d ações", totalActions), agent.ColorPurple)
				default:
					renderer.RenderBatchHeader(totalActions)
				}
			}

			// Iterar sobre TODAS as chamadas de ferramenta sugeridas
			for i, tc := range toolCalls {
				// --- SECURITY CHECK START ---
				if a.isCoderMode {
					pm, err := coder.NewPolicyManager(a.logger)
					if err == nil {
						action := pm.Check(tc.Name, tc.Args)
						if rule, ok := pm.LastMatchedRule(); ok {
							a.lastPolicyMatch = &rule
						} else {
							a.lastPolicyMatch = nil
						}
						if action == coder.ActionDeny {
							msg := "AÇÃO BLOQUEADA (Regra de Segurança)"
							renderError(msg)
							a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: "ERRO: " + msg})
							batchHasError = true
							break
						}
						if action == coder.ActionAsk {
							decision := coder.PromptSecurityCheckGuarded(ctx, tc.Name, tc.Args, a.stdinLines)
							pattern := coder.GetSuggestedPattern(tc.Name, tc.Args)
							switch decision {
							case coder.DecisionAllowAlways:
								if pattern != "" {
									_ = pm.AddRule(pattern, coder.ActionAllow)
								}
							case coder.DecisionDenyForever:
								if pattern != "" {
									_ = pm.AddRule(pattern, coder.ActionDeny)
								}
								msg := "AÇÃO BLOQUEADA PERMANENTEMENTE"
								renderError(msg)
								a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: "ERRO: " + msg})
								batchHasError = true
							case coder.DecisionDenyOnce:
								msg := "AÇÃO NEGADA PELO USUÁRIO"
								renderError(msg)
								a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: "ERRO: " + msg})
								batchHasError = true
							case coder.DecisionCanceled:
								msg := "OPERAÇÃO CANCELADA (Ctrl+C)"
								renderError(msg)
								a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: msg})
								batchHasError = true
							}
							if batchHasError {
								break
							}
						}
					}
				}
				// --- SECURITY CHECK END ---
				toolName := tc.Name
				toolArgsStr := tc.Args

				// Build compact label for aru-style display
				toolSubcmd := extractSubcmdFromArgs(toolArgsStr)
				compactLabel := agent.CompactToolLabel(toolSubcmd, toolArgsStr)
				toolStartTime := time.Now()

				// UX: Pequena pausa para separar visualmente o pensamento da ação
				if !isCompact {
					time.Sleep(200 * time.Millisecond)
				}

				// 2. Renderiza a BOX de ação IMEDIATAMENTE (antes de processar)
				if isCompact {
					renderer.CompactToolStart(compactLabel)
				} else if isMinimal {
					renderer.RenderToolCallMinimal(toolName, toolArgsStr, i+1, totalActions)
				} else {
					renderer.RenderToolCallWithProgress(toolName, toolArgsStr, i+1, totalActions)
				}

				// UX: Força flush e pausa para leitura
				_ = os.Stdout.Sync()
				if !isCompact {
					time.Sleep(300 * time.Millisecond)
				}

				// --- Lógica de Sanitização e Validação ---
				normalizedArgsStr := sanitizeToolCallArgs(toolArgsStr, a.logger, toolName, a.isCoderMode)

				// /coder mode: if args have newlines, try compacting JSON before failing.
				// Many AI models send pretty-printed JSON which is perfectly valid but multiline.
				if a.isCoderMode && hasAnyNewline(normalizedArgsStr) {
					compacted := tryCompactJSON(normalizedArgsStr)
					if compacted != "" && !hasAnyNewline(compacted) {
						// Successfully compacted multiline JSON into single line
						if a.logger != nil {
							a.logger.Debug("Compacted multiline JSON args to single line",
								zap.String("tool", toolName))
						}
						normalizedArgsStr = compacted
					} else {
						// Could not compact - enforce single line as before
						msg := buildCoderSingleLineArgsEnforcementPrompt(toolArgsStr)
						renderError("Format error: args contain line breaks")

						a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: msg})
						batchHasError = true
						break
					}
				}

				toolArgs, parseErr := parseToolArgsWithJSON(normalizedArgsStr)
				var toolOutput string
				var execErr error

				// --- Preparação da Execução ---
				// MCP tools handle their own arg parsing (JSON), so check them BEFORE parseErr
				if a.cli.mcpManager != nil && strings.HasPrefix(toolName, "mcp_") {
					// MCP tool dispatch — strip prefix and route to MCP server
					mcpToolName := strings.TrimPrefix(toolName, "mcp_")
					if a.cli.mcpManager.IsMCPTool(mcpToolName) {
						// Parse args into map for MCP
						mcpArgs := make(map[string]interface{})
						for i := 0; i < len(toolArgs)-1; i += 2 {
							mcpArgs[toolArgs[i]] = toolArgs[i+1]
						}
						// Also try JSON parsing if single arg
						if len(toolArgs) == 1 {
							_ = json.Unmarshal([]byte(toolArgs[0]), &mcpArgs)
						}
						// If toolArgs came from JSON parsing (parseToolArgsWithJSON), they're key=value pairs
						// Try re-parsing from normalized args string
						if len(mcpArgs) == 0 {
							_ = json.Unmarshal([]byte(normalizedArgsStr), &mcpArgs)
						}

						// Deferred schema: when the model invokes with empty args we
						// either fall through and execute (the tool legitimately takes
						// no input — e.g. list_allowed_directories) or, if the tool
						// declares required parameters we have not received, we return
						// the full schema so the model can re-invoke with the right
						// arguments.
						needsSchema := false
						if len(mcpArgs) == 0 {
							schema := a.cli.mcpManager.GetToolSchema(mcpToolName)
							if mcpToolHasRequiredParams(schema) {
								schemaJSON, _ := json.MarshalIndent(schema, "", "  ")
								toolOutput = fmt.Sprintf("MCP tool '%s' requires parameters. Here is the schema:\n%s\n\nPlease invoke again with the correct arguments.", mcpToolName, string(schemaJSON))
								needsSchema = true
							}
						}
						if needsSchema {
							// schema returned to the model; skip execution this turn
						} else {
							a.cli.animation.StopThinkingAnimation()
							if !isCompact {
								renderer.RenderStreamBoxStart("🔌", fmt.Sprintf("MCP: %s", mcpToolName), agent.ColorPurple)
							}

							// Audit trail: emit an info-level log line so operators
							// can grep the chatcli log for every auto-approved MCP
							// call (autoApprove / alwaysAllow / Trust=true). The
							// invocation still happens unconditionally — this
							// preserves the prior "MCP tools execute autonomously
							// in agent mode" contract while making the decision
							// visible. When an explicit MCP approval gate is
							// added, callers should branch on this same helper.
							if a.cli.mcpManager.ShouldAutoApprove(mcpToolName) {
								a.logger.Info("MCP tool auto-approved by config",
									zap.String("tool", mcpToolName),
									zap.Bool("coder_mode", a.isCoderMode))
							}

							result, mcpErr := a.cli.mcpManager.ExecuteTool(ctx, mcpToolName, mcpArgs)
							if mcpErr != nil {
								execErr = mcpErr
								toolOutput = fmt.Sprintf("MCP tool error: %v", mcpErr)
							} else {
								toolOutput = result.Content
								if result.IsError {
									execErr = fmt.Errorf("MCP tool returned error")
								}
							}

							if !isCompact {
								renderer.RenderStreamBoxEnd(agent.ColorPurple)
							}
						}

						if ctx.Err() != nil {
							return ctx.Err()
						}
					} else {
						execErr = fmt.Errorf("MCP tool não encontrado")
						toolOutput = fmt.Sprintf("Ferramenta MCP '%s' não existe ou servidor desconectado.", mcpToolName)
					}
				} else if parseErr != nil {
					// Non-MCP tool with parse error
					execErr = parseErr
					toolOutput = fmt.Sprintf("Args parsing error: %v", parseErr)

					if a.isCoderMode {
						fixMsg := fmt.Sprintf("ERROR: Your <tool_call> has invalid args (error: %v). In /coder mode, args MUST be valid single-line JSON.", parseErr)
						fixMsg += "\n\nQuick fix - use one of these formats:\n" +
							`<tool_call name="@coder" args='{"cmd":"read","args":{"file":"main.go"}}' />` + "\n" +
							`<tool_call name="@coder" args='{"cmd":"exec","args":{"cmd":"go test ./..."}}' />` + "\n" +
							`<tool_call name="@coder" args='{"cmd":"write","args":{"file":"out.go","content":"BASE64","encoding":"base64"}}' />` + "\n" +
							`<tool_call name="@coder" args="read --file main.go" />`
						a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: fixMsg})
						batchHasError = true
					}
				} else {
					plugin, found := a.cli.pluginManager.GetPlugin(toolName)
					if !found {
						execErr = fmt.Errorf("plugin não encontrado")
						toolOutput = fmt.Sprintf("Ferramenta '%s' não existe ou não está instalada.", toolName)

						if a.isCoderMode {
							var available []string
							for _, p := range a.cli.pluginManager.GetPlugins() {
								available = append(available, p.Name())
							}
							a.cli.history = append(a.cli.history, models.Message{
								Role:    "user",
								Content: fmt.Sprintf("Tool %q not found. Available: %s", toolName, strings.Join(available, ", ")),
							})
							batchHasError = true
						}
					} else {
						// Guard-rail do /coder (@coder) - Argumentos obrigatórios
						if a.isCoderMode && strings.EqualFold(strings.TrimSpace(toolName), "@coder") {
							if missing, which := isCoderArgsMissingRequiredValue(toolArgs); missing {
								msg := buildCoderToolCallFixPrompt(which)
								renderError("Args inválido: falta " + which)

								a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: msg})
								batchHasError = true
								// Marca erro para parar o loop
								execErr = fmt.Errorf("argumento obrigatório faltando: %s", which)
							}
						}

						// --- DANGEROUS EXEC GUARD ---
						// Even if policy says "allow", NEVER auto-execute dangerous commands.
						// This catches cases where user clicked "Allow Always" for @coder exec.
						if a.isCoderMode && !batchHasError {
							if dangerous, shellCmd := a.isCoderExecDangerous(toolArgs); dangerous {
								msg := fmt.Sprintf(
									"BLOCKED: Dangerous command detected in @coder exec: %q. "+
										"This command is forbidden regardless of policy rules. "+
										"DO NOT retry this command.", shellCmd)
								renderError(msg)
								a.cli.history = append(a.cli.history, models.Message{
									Role: "user", Content: "SECURITY BLOCK: " + msg,
								})
								batchHasError = true
								execErr = fmt.Errorf("dangerous command blocked: %s", shellCmd)
							}
						}
						// --- END DANGEROUS EXEC GUARD ---

						// Se não houve erro de validação, EXECUTA
						if !batchHasError {
							// Fire PreToolUse hook — may block the action
							if a.cli.hookManager != nil {
								wd, _ := os.Getwd()
								hookResult := a.cli.hookManager.Fire(hooks.HookEvent{
									Type:       hooks.EventPreToolUse,
									Timestamp:  time.Now(),
									ToolName:   toolName,
									ToolArgs:   normalizedArgsStr,
									SessionID:  a.cli.currentSessionName,
									WorkingDir: wd,
								})
								if hookResult != nil && hookResult.Blocked {
									msg := fmt.Sprintf("BLOCKED by hook: %s", hookResult.BlockReason)
									renderError(msg)
									a.cli.history = append(a.cli.history, models.Message{
										Role: "user", Content: "HOOK BLOCK: " + msg,
									})
									batchHasError = true
									execErr = fmt.Errorf("blocked by hook: %s", hookResult.BlockReason)
								}
							}
						}

						if !batchHasError {
							// Per-call spinner label (Item 7): use the
							// plugin's DescribeCall to surface the actual
							// target (file, URL, regex, …) instead of the
							// generic "EXECUTANDO: @tool subcmd". Falls back
							// to the legacy shape for plugins that don't
							// implement DescriberWithInput or when the args
							// can't be parsed.
							a.cli.animation.StopThinkingAnimation()

							boxLabel := defaultSpinnerLabel(toolName, toolArgs)
							if p, ok := a.cli.pluginManager.GetPlugin(tc.Name); ok && p != nil {
								if d := plugins.DescribeCall(p, toolArgs); d != "" {
									boxLabel = d
								}
							}

							if !isCompact {
								renderer.RenderStreamBoxStart("🔨", boxLabel, agent.ColorPurple)
							}

							streamCallback := func(line string) {
								if !isCompact {
									renderer.StreamOutput(line)
								}
							}

							// Marca tarefa como em andamento ANTES de executar
							agent.MarkTaskInProgress(a.taskTracker)

							// Delegate interception: if this is an @coder call with
							// cmd=delegate, it's a subagent spawn — NOT a plugin
							// invocation. Route to workers.RunDelegate with our
							// LLM client so the subagent has its own isolated
							// context.
							if strings.EqualFold(strings.TrimSpace(toolName), "@coder") && isDelegateInvocation(normalizedArgsStr) {
								nativeArgs, rawInner := extractDelegateArgs(normalizedArgsStr)
								toolOutput, execErr = workers.RunDelegate(
									ctx,
									nativeArgs,
									rawInner,
									a.cli.Client,
									nil, // no file lock manager at top level — subagent uses its own
									nil, // no skills propagation for now
									nil, // policy handled upstream already
									a.logger,
								)
							} else {
								// Schema validation gate (Item 5): if the plugin
								// implements JSONSchemaAware, validate the args
								// envelope before dispatch. A failure becomes a
								// fast InvalidArgs IsError so the model sees the
								// schema violation cleanly instead of a panic
								// or empty-string-return deep inside the plugin.
								// Plugins that do not implement the interface
								// bypass this gate entirely — purely additive.
								if vErr := plugins.ValidateArgs(plugin, normalizedArgsStr); vErr != nil {
									toolOutput = vErr.Error()
									execErr = vErr
								} else {
									toolOutput, execErr = plugin.ExecuteWithStream(ctx, toolArgs, streamCallback)
								}
							}

							if !isCompact {
								renderer.RenderStreamBoxEnd(agent.ColorPurple)
							}

							// Se o contexto foi cancelado (Ctrl+C), propaga imediatamente
							if ctx.Err() != nil {
								return ctx.Err()
							}

							// Park sentinel: a tool (@park) asked the loop to
							// suspend. We snapshot at this exact point — the
							// assistant message with the @park tool_use is
							// already in history (line 1318/1324) — and enqueue
							// the scheduler-driven resume. The sentinel bubbles
							// out of the loop; Run() reads it via errors.Is
							// and returns nil to the user.
							//
							// The box footer was already rendered above (line
							// 2053). Don't repeat it here — that produced the
							// double-close that landed in the user's terminal
							// as two `╰────╮` rows.
							if req, parked := park.AsParkError(execErr); parked {
								var pendingID string
								if i < len(nativeToolCalls) {
									pendingID = nativeToolCalls[i].ID
								}
								return a.handleAgentPark(ctx, req, pendingID, toolName)
							}
						}
					}
				}

				// 3. Renderiza resultado individual (após a execução)
				displayForHuman := toolOutput
				if execErr != nil {
					errText := execErr.Error()
					if strings.TrimSpace(displayForHuman) == "" {
						displayForHuman = errText
					} else {
						displayForHuman = displayForHuman + "\n\n--- ERRO ---\n" + errText
					}
				}
				if isCompact {
					elapsed := time.Since(toolStartTime)
					durationStr := ""
					if elapsed >= 500*time.Millisecond {
						durationStr = fmt.Sprintf("%.1fs", elapsed.Seconds())
					}
					renderer.CompactToolDone(compactLabel, durationStr, execErr != nil)
				} else if isMinimal {
					renderer.RenderToolResultMinimal(displayForHuman, execErr != nil)
				} else {
					renderer.RenderToolResult(displayForHuman, execErr != nil)
				}

				// Fire PostToolUse / PostToolUseFailure hooks
				if a.cli.hookManager != nil {
					wd, _ := os.Getwd()
					eventType := hooks.EventPostToolUse
					errStr := ""
					if execErr != nil {
						eventType = hooks.EventPostToolUseFailure
						errStr = execErr.Error()
					}
					// Truncate output for hook payload
					hookOutput := toolOutput
					if len(hookOutput) > 2000 {
						hookOutput = hookOutput[:2000] + "...(truncated)"
					}
					a.cli.hookManager.FireAsync(hooks.HookEvent{
						Type:       eventType,
						Timestamp:  time.Now(),
						ToolName:   toolName,
						ToolArgs:   normalizedArgsStr,
						ToolOutput: hookOutput,
						Error:      errStr,
						SessionID:  a.cli.currentSessionName,
						WorkingDir: wd,
					})
				}

				// Atualiza status da tarefa e re-renderiza plano
				if execErr != nil {
					agent.MarkTaskFailed(a.taskTracker, execErr.Error())
				} else {
					agent.MarkTaskCompleted(a.taskTracker)
				}
				renderPlanProgress()

				// Capture the structured outcome for downstream phases.
				// Duration is wall-clock for this single tool — Fase 3 will
				// also use it to size the per-batch concurrency budget.
				structured := agent.WrapLegacyOutput(toolOutput, execErr)
				structured.Duration = time.Since(toolStartTime)
				turnToolResults = append(turnToolResults, structured)

				// Acumula o resultado para a LLM
				batchOutputBuilder.WriteString(fmt.Sprintf("--- Resultado da Ação %d (%s) ---\n", i+1, toolName))

				if execErr != nil || batchHasError {
					batchOutputBuilder.WriteString(fmt.Sprintf("ERRO: %v\nSaída parcial: %s\n", execErr, toolOutput))
					batchOutputBuilder.WriteString("\n[EXECUÇÃO EM LOTE INTERROMPIDA PREMATURAMENTE DEVIDO A ERRO NA AÇÃO ANTERIOR]\n")

					// Garante flag de erro se veio de execErr
					batchHasError = true
					break // Fail-Fast: Para a execução do lote
				} else {
					// Per-tool truncation (Item 6). Plugins that
					// implement plugins.TruncationAware get their own
					// per-call cap; the rest use the global default.
					// We look the plugin up again here because the
					// inner-scope `plugin` variable from the dispatch
					// block is no longer in scope at this site.
					maxChars := plugins.DefaultMaxResultChars
					if p, ok := a.cli.pluginManager.GetPlugin(tc.Name); ok && p != nil {
						maxChars = plugins.EffectiveMaxResultChars(p)
					}
					toolOutput = plugins.TruncateForLLM(toolOutput, maxChars)

					batchOutputBuilder.WriteString(toolOutput)
					batchOutputBuilder.WriteString("\n\n")
					successCount++
					a.toolCallsExecd++
					turnToolCalls++
				}
			}

			// Log per-batch structured outcome at DEBUG so operators can
			// trace error-code distribution and timing without parsing the
			// LLM-facing concatenation. The slice is also surfaced via
			// a.lastTurnToolResults for in-process consumers (the provider
			// adapter pipeline in Fase 5).
			a.lastTurnToolResults = turnToolResults
			if a.logger != nil && len(turnToolResults) > 0 {
				var errCodes []string
				var totalDur time.Duration
				for _, r := range turnToolResults {
					if r.IsError {
						errCodes = append(errCodes, r.ErrorCode)
					}
					totalDur += r.Duration
				}
				a.logger.Debug("tool batch completed",
					zap.Int("total", len(turnToolResults)),
					zap.Int("success", successCount),
					zap.Strings("error_codes", errCodes),
					zap.Duration("total_duration", totalDur))
			}

			// 4. Renderiza rodapé do lote
			if totalActions > 1 {
				if isCompact {
					renderer.CompactBatchSummary(successCount, totalActions, batchHasError)
				} else {
					renderer.RenderBatchSummary(successCount, totalActions, batchHasError)
				}
			}

			// Lógica de Continuação:
			// Se houve erro de validação (onde já inserimos msg específica no histórico) E nenhuma ação rodou,
			// apenas damos continue para a IA tentar corrigir.
			if batchHasError && !strings.Contains(batchOutputBuilder.String(), "Resultado da Ação") {
				continue
			}

			// Provider-agnostic tool_result emission: when the assistant
			// produced native tool_calls in this turn AND every call has
			// a matching structured result, emit one models.Message with
			// Role="tool" per call (ToolCallID + IsError + ErrorCode
			// flowing through). Each provider adapter
			// (claudeai, openai, moonshot, minimax, zai, openrouter)
			// already knows how to translate role="tool" into its
			// native shape — Anthropic tool_result with is_error,
			// OpenAI-family tool message with [ERROR:<code>] marker.
			//
			// We keep the legacy concatenated user message ONLY when
			// the assistant did not produce native tool_calls (text-
			// mode XML dispatch path), so providers that don't carry
			// tool_use IDs still see the batch output for context.
			useStructured := len(nativeToolCalls) > 0 &&
				len(turnToolResults) == len(nativeToolCalls) &&
				!batchHasError // mid-batch infra error doesn't produce a full result slice

			if useStructured {
				for i, res := range turnToolResults {
					content := res.Output
					// Per-tool truncation already applied in the loop.
					a.cli.history = append(a.cli.history,
						models.NewToolResultMessage(
							nativeToolCalls[i].ID,
							content,
							res.IsError,
							res.ErrorCode,
						))
				}
				// Optional replanning hint as a side note, kept as a
				// system-style nudge rather than a malformed user message.
				if a.taskTracker != nil && a.taskTracker.NeedsReplanning() {
					a.cli.history = append(a.cli.history, models.Message{
						Role:    "user",
						Content: "ATENÇÃO: Múltiplas falhas detectadas. Crie um NOVO <reasoning> com uma lista replanejada de tarefas, considerando os erros anteriores.",
					})
				}
			} else {
				// Legacy path: text-mode dispatch or partially-failed batch.
				// One user message carries everything; provider adapters
				// see plain text without tool_result block semantics.
				feedbackForAI := i18n.T("agent.feedback.tool_output", "batch_execution", batchOutputBuilder.String())
				if a.taskTracker != nil && a.taskTracker.NeedsReplanning() {
					feedbackForAI += "\n\nATENÇÃO: Múltiplas falhas detectadas. Crie um NOVO <reasoning> com uma lista replanejada de tarefas, considerando os erros anteriores."
				}
				a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: feedbackForAI})
			}

			showTurnStats()
			continue
		}

		// =========================================================
		// PRIORIDADE 2: EXECUTE BLOCKS (Legado / Modo Agente Padrão)
		// =========================================================
		commandBlocks := a.extractCommandBlocks(aiResponse)
		if len(commandBlocks) > 0 {
			if a.isCoderMode && a.isOneShot {
				a.cli.history = append(a.cli.history, models.Message{
					Role: "user",
					Content: "Você respondeu com comandos em bloco (shell). No modo /coder você DEVE usar <tool_call> " +
						"para executar ferramentas/plugins (especialmente @coder). " +
						"Reenvie a próxima ação SOMENTE como <tool_call name=\"@coder\" ... /> (sem blocos ```).",
				})
				continue
			}

			if a.isCoderMode {
				a.cli.history = append(a.cli.history, models.Message{
					Role: "user",
					Content: "No modo /coder, não use blocos ```execute``` nem comandos shell. " +
						"Use <reasoning> e então emita <tool_call name=\"@coder\" ... />.",
				})
				continue
			}

			renderMDCard("🧩", "PLANO GERADO", "A IA gerou um plano de ação com comandos executáveis. Use o menu abaixo para executar.", agent.ColorLime)
			a.handleCommandBlocks(ctx, commandBlocks)
			return nil
		}

		// ==========================================
		// PRIORIDADE 3: RESPOSTA FINAL (sem ações)
		// ==========================================

		// In coder mode, the AI may respond without tool calls when it needs
		// information from the user (e.g., "What role should I use?", "Which
		// file do you mean?"). Instead of exiting, wait for user input so the
		// conversation can continue.
		if a.isCoderMode && !a.isOneShot {
			showTurnStats()
			fmt.Println()
			fmt.Print(renderer.Colorize("  ⏳ "+i18n.T("coder.waiting_for_input"), agent.ColorCyan))
			fmt.Println() // newline before input for clean cursor positioning

			// Stop the raw stdin reader so we can use line-editing input.
			// The raw reader captures escape sequences as literal bytes (^[[A for arrows),
			// making it impossible to navigate text. We temporarily switch to
			// golang.org/x/term which provides full readline support.
			hadStdinReader := a.stdinLines != nil
			if hadStdinReader {
				a.stopStdinReader()
			}

			userInput, err := a.readLineWithEditing()

			// Restart the stdin reader for subsequent agent turns
			if hadStdinReader {
				a.startStdinReader()
			}
			if err != nil {
				return err
			}
			userInput = strings.TrimSpace(userInput)

			// Allow the user to exit explicitly
			if userInput == "" || strings.EqualFold(userInput, "exit") || strings.EqualFold(userInput, "quit") || strings.EqualFold(userInput, "sair") {
				fmt.Println(renderer.Colorize("\n"+i18n.T("agent.status.task_completed"), agent.ColorGreen+agent.ColorBold))
				return nil
			}

			// Persistent echo in green/❯: the kernel echo during
			// line-editing is uncolored, and once the line commits it
			// scrolls between gray tool prose. The echo gives the
			// user's directive a distinct lane so it's findable when
			// scrolling back through the timeline. Coder-only because
			// /agent uses single-letter menu input that the prompt
			// already highlights.
			if a.isCoderMode && renderer.IsCompact() {
				renderer.EchoUserInput(userInput)
			}

			a.cli.history = append(a.cli.history, models.Message{
				Role:    "user",
				Content: userInput,
			})
			continue
		}

		showTurnStats()
		fmt.Println(renderer.Colorize("\n"+i18n.T("agent.status.task_completed"), agent.ColorGreen+agent.ColorBold))
		return nil
	}

	fmt.Println(renderer.Colorize(
		"\n"+i18n.T("agent.status.max_turns_stopped", maxTurns),
		agent.ColorYellow,
	))
	return nil
}

func (a *AgentMode) continueWithNewAIResponse(ctx context.Context) {
	turns := AgentMaxTurns()

	err := a.processAIResponseAndAct(ctx, turns)
	// Park is success, not an error to surface to the user.
	if errors.Is(err, errAgentParkedRequested) {
		return
	}
	if err != nil {
		fmt.Println(colorize(
			i18n.T("agent.error.continuation_failed", err),
			ColorYellow,
		))
	}
}

// helper max turns
// splitToolArgsMultiline faz split de argv estilo shell, mas com suporte a multilinha.
// Regras:
// - separa por whitespace (inclui \n) quando NÃO estiver dentro de aspas
// - suporta aspas simples e duplas
// - permite newline dentro de aspas (vira parte do mesmo argumento)
// - "\" funciona como escape fora de aspas simples (ex: \" ou \n literal etc.)
// - não interpreta sequências como \n => newline; mantém literal \ + n (quem interpreta é o plugin, se quiser)
// - retorna erro se aspas não balanceadas ou escape pendente no final
func (a *AgentMode) initMultiAgent() bool {
	modeStr := strings.TrimSpace(strings.ToLower(os.Getenv("CHATCLI_AGENT_PARALLEL_MODE")))
	if modeStr == "false" || modeStr == "0" {
		a.parallelMode = false
		return false
	}

	// Registry already initialized — just update provider/model in case
	// the user switched providers at runtime.
	if a.agentRegistry != nil {
		a.parallelMode = true
		if a.agentDispatcher != nil {
			provider := a.cli.Provider
			if provider == "" {
				provider = os.Getenv("LLM_PROVIDER")
			}
			if provider == "" {
				provider = config.Global.GetString("LLM_PROVIDER")
			}
			model := a.cli.Model
			a.agentDispatcher.UpdateProviderModel(provider, model)
			a.logger.Info("Dispatcher provider/model updated for parallel agents",
				zap.String("provider", provider),
				zap.String("model", model),
			)
		}
		return true
	}

	a.agentRegistry = workers.SetupDefaultRegistry()

	// Load custom persona agents into the worker registry
	if a.cli.personaHandler != nil {
		mgr := a.cli.personaHandler.GetManager()
		if customCount := workers.LoadCustomAgents(a.agentRegistry, mgr, a.logger); customCount > 0 {
			a.logger.Info("Custom persona agents loaded as workers",
				zap.Int("count", customCount),
			)
		}
	}

	a.fileLockMgr = workers.NewFileLockManager()

	maxWorkersStr := os.Getenv("CHATCLI_AGENT_MAX_WORKERS")
	maxWorkers := workers.DefaultMaxWorkers
	if maxWorkersStr != "" {
		if v, err := strconv.Atoi(maxWorkersStr); err == nil && v > 0 {
			maxWorkers = v
		}
	}

	workerTimeout := workers.DefaultWorkerTimeout
	if ts := os.Getenv("CHATCLI_AGENT_WORKER_TIMEOUT"); ts != "" {
		if d, err := time.ParseDuration(ts); err == nil && d > 0 {
			workerTimeout = d
		}
	}

	// Determine provider/model from current active client (not defaults).
	// a.cli.Provider is the runtime-resolved provider (from -provider flag, env, or config).
	// Falling back to env/config only if cli.Provider is somehow empty.
	provider := a.cli.Provider
	if provider == "" {
		provider = os.Getenv("LLM_PROVIDER")
	}
	if provider == "" {
		provider = config.Global.GetString("LLM_PROVIDER")
	}
	// Use a.cli.Model (the actual API model ID, e.g. "claude-sonnet-4-6-20250514")
	// NOT a.cli.Client.GetModelName() which returns a display name (e.g. "Claude sonnet 4.6").
	model := a.cli.Model

	cfg := workers.DispatcherConfig{
		MaxWorkers:    maxWorkers,
		ParallelMode:  true,
		Provider:      provider,
		Model:         model,
		WorkerTimeout: workerTimeout,
	}

	a.agentDispatcher = workers.NewDispatcher(a.agentRegistry, a.cli.manager, cfg, a.logger)

	// Attach policy enforcement so parallel workers respect security rules
	if pa, err := newWorkerPolicyAdapter(a.logger); err == nil {
		a.policyAdapter = pa
		a.agentDispatcher.SetPolicyChecker(pa)
		a.logger.Info("Policy enforcement enabled for parallel workers")
	} else {
		a.logger.Warn("Failed to initialize policy checker for parallel workers", zap.Error(err))
	}

	// Attach the seven-pattern quality pipeline (Self-Refine, CoVe,
	// Reflexion, …). Pipeline starts with the hooks selected by the
	// CHATCLI_QUALITY_* env, with /refine and /verify session toggles
	// layered on top. With zero hooks (the default), Run is a thin
	// pass-through to agent.Execute — no measurable overhead.
	//
	// dispatchOne lets quality hooks invoke other agents (Refiner,
	// Verifier) without taking a direct dependency on the dispatcher.
	// Built-in ExcludeAgents prevent the obvious infinite-recursion
	// case (refining the refiner's output, verifying the verifier's).
	a.qualityConfig = quality.LoadFromEnv()
	if a.cli.qualityOverrides.Refine != nil {
		a.qualityConfig.Refine.Enabled = *a.cli.qualityOverrides.Refine
	}
	if a.cli.qualityOverrides.Verify != nil {
		a.qualityConfig.Verify.Enabled = *a.cli.qualityOverrides.Verify
	}
	dispatchOne := func(ctx context.Context, call workers.AgentCall) workers.AgentResult {
		results := a.agentDispatcher.Dispatch(ctx, []workers.AgentCall{call})
		if len(results) == 0 {
			return workers.AgentResult{
				CallID: call.ID, Agent: call.Agent, Task: call.Task,
				Error: fmt.Errorf("dispatcher returned no result for quality hook"),
			}
		}
		return results[0]
	}
	// Reflexion (Phase 4) needs an LLM call + a memory-persist
	// callback. Both are wired here so the pipeline package stays
	// independent of cli.ChatCLI internals.
	//
	// When the durable lesson queue is enabled in config, we also
	// build (lazily) a lessonq.Runner and inject its enqueuer. The
	// Runner owns a WAL + worker pool + DLQ so reflexion triggers
	// survive process crashes; see cli/reflexion_setup.go.
	lessonLLM := a.cli.makeLessonLLM()
	persistLesson := a.cli.makeLessonPersister()
	enqueuer := a.cli.reflexionEnqueuer(a.qualityConfig.Reflexion.Queue)
	convChecker := a.cli.buildRefineConvergence(a.qualityConfig)
	a.qualityPipeline = quality.BuildPipeline(a.qualityConfig, a.logger, quality.BuildPipelineDeps{
		Dispatch:           dispatchOne,
		LessonLLM:          lessonLLM,
		PersistLesson:      persistLesson,
		LessonEnqueuer:     enqueuer,
		ConvergenceChecker: convChecker,
	})
	a.agentDispatcher.SetPipeline(a.qualityPipeline)

	a.parallelMode = true

	a.logger.Info("Multi-agent orchestration enabled",
		zap.Int("maxWorkers", maxWorkers),
		zap.Duration("workerTimeout", workerTimeout),
		zap.String("provider", provider),
		zap.String("model", model),
	)

	return true
}

// buildSessionWorkspaceHint returns a compact prompt block that teaches the
// buildMCPToolsSection renders the system-prompt block listing MCP
// tools available this turn, plus the routing hint that biases the
// model toward `mcp_*` when an MCP server covers the requested op.
// Pure function so the rendering can be tested without spinning up an
// AgentMode instance and a live LLM client.
func buildMCPToolsSection(tools []models.ToolDefinition, isCoderMode bool) string {
	var b strings.Builder
	b.WriteString("MCP Tools (external):\n")
	b.WriteString("  Para invocar: <tool_call name=\"mcp_<tool>\" args='{\"param\":\"value\"}' />\n")
	b.WriteString("  Se precisar dos parâmetros exatos, invoque e o sistema retornará o schema.\n")
	if isCoderMode {
		b.WriteString("\n  " + i18n.T("agent.mcp.routing_hint_coder") + "\n")
	} else {
		b.WriteString("\n  " + i18n.T("agent.mcp.routing_hint_agent") + "\n")
	}
	b.WriteString("\n")
	for _, t := range tools {
		b.WriteString(fmt.Sprintf("  - %s: %s\n", t.Function.Name, t.Function.Description))
	}
	return b.String()
}

// buildMCPEmptyNote returns the system-prompt note shown to the model
// when MCP is configured but no tool is usable yet — either still
// starting or every server failed to start. Returns empty string when
// no server is configured at all (so callers can append unconditionally).
func buildMCPEmptyNote(statuses []mcp.ServerStatus) string {
	if len(statuses) == 0 {
		return ""
	}
	for _, s := range statuses {
		if s.Starting {
			return i18n.T("agent.mcp.note_starting") + "\n"
		}
	}
	return i18n.T("agent.mcp.note_unavailable") + "\n"
}

// mcpToolHasRequiredParams reports whether a JSON schema declares any
// required input parameter. Used to distinguish a tool that legitimately
// accepts {} (e.g. list_allowed_directories) from one whose call lost
// its arguments and needs the schema sent back to the model.
func mcpToolHasRequiredParams(schema map[string]interface{}) bool {
	if schema == nil {
		return false
	}
	required, ok := schema["required"].([]interface{})
	if !ok || len(required) == 0 {
		return false
	}
	return true
}

// model how to use the per-session scratch dir and how to recover data from
// truncated tool results. Returns an empty string when the session workspace
// hasn't been initialized (shouldn't happen during normal startup).
func buildSessionWorkspaceHint() string {
	ws := agent.GetSessionWorkspace()
	if ws == nil {
		return ""
	}
	return "\n\n## SESSION WORKSPACE & LARGE OUTPUTS\n" +
		"\n" +
		"You have an isolated scratch directory for this session, exposed via the\n" +
		"environment variable `CHATCLI_AGENT_TMPDIR` (current value: `" + ws.ScratchDir + "`).\n" +
		"Both read and write are ALLOWED in this directory and in its subtree —\n" +
		"use it whenever you need to:\n" +
		"- stage a temporary shell script before exec'ing it;\n" +
		"- persist an intermediate artifact between tool calls;\n" +
		"- avoid polluting the project tree with one-off files.\n" +
		"\n" +
		"Example: `exec { \"cmd\": \"cat > $CHATCLI_AGENT_TMPDIR/patch.sh <<'EOF' ...\" }`.\n" +
		"\n" +
		"### Truncated tool outputs\n" +
		"\n" +
		"When a tool result is large, ChatCLI automatically truncates it inline\n" +
		"and saves the FULL output to a file in this session. You will see a\n" +
		"marker like:\n" +
		"\n" +
		"    ... [N chars omitted — full output saved to /tmp/chatcli-agent-XXX/tool-results/budget_xxx.txt]\n" +
		"    ... [full output saved to /tmp/chatcli-agent-XXX/tool-results/result_XXX.txt — N bytes total]\n" +
		"\n" +
		"When you see such a marker and the omitted portion matters, use the\n" +
		"`read_file` tool with `start` / `end` line numbers to examine specific\n" +
		"ranges of the saved file — do NOT re-run the original tool call.\n" +
		"\n" +
		"### Delegating heavy analysis\n" +
		"\n" +
		"For tasks that would otherwise flood your context with raw data (large\n" +
		"metrics endpoints, verbose logs, wide-scope searches), use the\n" +
		"`delegate_subagent` tool. The subagent runs with its OWN isolated\n" +
		"context window; only its final summary returns to you. Example use\n" +
		"cases: \"summarize memory hotspots from /metrics\", \"find all call\n" +
		"sites of func X across the repo\".\n"
}

// isDelegateInvocation reports whether an @coder JSON args payload is a
// delegate_subagent call rather than a normal engine subcommand.
func isDelegateInvocation(argsJSON string) bool {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return false
	}
	var outer struct {
		Cmd string `json:"cmd"`
	}
	if err := json.Unmarshal([]byte(trimmed), &outer); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(outer.Cmd), "delegate")
}

// buildAgentSkillBlocks composes the agent-mode skill prompt block: pinned
// skills first (stable across turns), auto-activated skills next (volatile
// by query). The function also mutates a.skillModelHint and
// a.skillEffortHint when any skill set carries those frontmatter hints.
//
// Returns the assembled prompt text. Empty when no skills fire.
func (a *AgentMode) buildAgentSkillBlocks(query, additionalContext string) string {
	if a.cli.personaHandler == nil {
		return ""
	}
	mgr := a.cli.personaHandler.GetManager()
	if mgr == nil {
		return ""
	}

	var pinned []*persona.Skill
	if a.cli.skillHandler != nil {
		pinned = a.cli.skillHandler.GetPinnedSkills()
	}

	filePaths := extractFilePaths(query + " " + additionalContext)
	autoActivated := mgr.FindAutoActivatedSkills(query, filePaths)
	autoActivated = dedupAutoAgainstPinned(autoActivated, pinned)

	skillsText := concatSkillBlocks(pinned, autoActivated)

	merged := append([]*persona.Skill(nil), pinned...)
	merged = append(merged, autoActivated...)
	if len(merged) > 0 {
		model, effort, _ := pickSkillModelAndEffort(merged)
		if model != "" {
			a.skillModelHint = model
		}
		if effort != "" {
			a.skillEffortHint = llmclient.NormalizeEffort(effort)
		}
		a.logger.Info("agent mode: skills injected",
			zap.Int("pinned", len(pinned)),
			zap.Int("auto", len(autoActivated)),
			zap.String("model_hint", a.skillModelHint),
			zap.String("effort_hint", string(a.skillEffortHint)))
	}
	return skillsText
}

// concatSkillBlocks renders pinned skills (with the pinned-skill header)
// followed by auto-activated skills (with the auto-loaded header), joined
// by a blank line when both fire. Returns "" when neither slice produces
// content. Pure — extracted so callers in chat and agent mode share the
// concatenation rule and so tests can drive it without a ChatCLI fixture.
func concatSkillBlocks(pinned, autoActivated []*persona.Skill) string {
	var out string
	if len(pinned) > 0 {
		if block := buildPinnedSkillInjectionBlock(pinned); block != "" {
			out = block
		}
	}
	if len(autoActivated) > 0 {
		if block := buildSkillInjectionBlock(autoActivated); block != "" {
			if out != "" {
				out += "\n\n"
			}
			out += block
		}
	}
	return out
}

// extractDelegateArgs pulls the inner args map from an @coder delegate call.
// Returns (nativeArgsMap, rawJSONString) — the map is preferred when non-nil,
// and rawJSONString is a fallback for XML-style args.
func extractDelegateArgs(argsJSON string) (map[string]interface{}, string) {
	var outer struct {
		Cmd  string          `json:"cmd"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &outer); err != nil {
		return nil, argsJSON
	}
	if len(outer.Args) == 0 {
		return nil, ""
	}
	var inner map[string]interface{}
	if err := json.Unmarshal(outer.Args, &inner); err == nil {
		return inner, string(outer.Args)
	}
	return nil, string(outer.Args)
}
