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
	"unicode/utf8"

	"github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/agent/ask"
	"github.com/diillson/chatcli/cli/agent/mail"
	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/cli/agent/toolguard"
	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/hooks"
	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/metrics"
	"github.com/diillson/chatcli/cli/palette"
	"github.com/diillson/chatcli/cli/paste"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/persona"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
	"golang.org/x/term"
)

// AgentMode representa a funcionalidade de agente autônomo no ChatCLI
type AgentMode struct {
	// toolDefsChars is the serialized size of this run's native tool
	// definitions — request weight outside the history that the compaction
	// budget must still reserve (CompactConfig.ReservedChars).
	toolDefsChars       int
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
	// inlineParkToken names the park this run is waiting on IN-TURN, set by
	// handleAgentPark on unattended surfaces (ACP / MCP server / gateway)
	// where no REPL exists to drain the resume queue. Run() sees the park
	// sentinel, finds this token, and blocks in runParkedInline until the
	// scheduler delivers the wake — keeping the client's request open and
	// the event sink streaming instead of ending the turn into a resume
	// that could never be delivered.
	inlineParkToken string
	// pendingUserImages carries vision attachments for the NEXT user turn,
	// set by the caller right before Run/RunOnce and consumed (then cleared)
	// when the user message is appended. Transient per-turn state — the Run
	// reentrancy guard ensures it is never read across concurrent runs.
	pendingUserImages []models.ImageContent

	// stagedViewImages carries images the @view tool attached mid-run; they
	// are drained into a user message at the next turn boundary (the same
	// protocol-safety point as the mid-loop skill blocks). Mutex-guarded:
	// @view is read-only and may run inside a parallel batch.
	stagedViewMu     sync.Mutex
	stagedViewImages []models.ImageContent
	stagedViewNames  []string
	// gatewayPersona layers a messaging-gateway directive on top of the coder
	// system prompt: keep the coder engine's full tool capability (create/edit
	// files, run commands, iterate) but answer concisely in plain chat-friendly
	// text. Set by the gateway daemon's one-shot runs; never by interactive
	// /coder.
	gatewayPersona  bool
	lastPolicyMatch *coder.Rule
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
	stdinLines   chan string        // all stdin lines flow through here
	stdinDone    chan struct{}      // signals reader goroutine to stop
	stdinCancel  *stdinReadCanceler // aborts a blocking console read (Windows)
	stdinWg      sync.WaitGroup
	multilineBuf MultilineBuffer // ``` delimited multiline input

	// stdinMu guards the reader lifecycle fields above plus stdinDepth.
	// processAIResponseAndAct is re-entrant (command-block menu, park
	// resume) and each entry brackets the reader with start/stop; the
	// depth count keeps the INNER stop from tearing the reader down while
	// the outer loop still needs it (previously type-ahead silently died
	// for the rest of the run after any nested entry). Interactive
	// overlays that need the fd released outright use suspend/resume,
	// which bypass the refcount.
	stdinMu    sync.Mutex
	stdinDepth int

	// Side commands typed mid-run (/agents, /board, /mail, /jobs) that
	// could not run immediately (live display not active — e.g. a security
	// prompt owns the terminal). Applied at the next turn boundary.
	sideCmdMu    sync.Mutex
	sideCmdQueue []string
	// sideCmdExec runs one side command line; a seam so tests can capture
	// executions. Defaults to routing through the command handler.
	sideCmdExec func(string)

	// Live type-ahead preview: the partial line typed since the last
	// Enter, published by the reader goroutine and rendered by the spinner
	// and the dispatch panel. Only populated while the TTY is in cbreak
	// mode (Unix); empty on Windows and non-TTY runs.
	typeaheadMu   sync.Mutex
	typeaheadLine string
	// stdinCbreakRestore undoes the cbreak TTY state applied when the
	// reader spawned. Guarded by stdinMu.
	stdinCbreakRestore func()

	// lastBoardNudge dedups the [BOARD SYNC] reconciliation block: the
	// model gets one nudge per distinct stale-board state, not one per
	// turn. Reset at Run() start.
	lastBoardNudge string

	// cancelSignal is the Done() channel of the in-flight Run/ReAct loop's
	// context. Blocking stdin reads (security confirmations, batch prompts)
	// select on it so a Ctrl+C aborts them the instant the operation is
	// cancelled — instead of hanging on the channel receive until the user
	// presses Enter. That hang previously left the terminal in cooked mode
	// and the centralized reader half-torn-down, which corrupted the next
	// REPL prompt (the "/coder enters but nothing fires" bug). We store only
	// the cancellation channel, not the context.Context: the reader needs the
	// signal, nothing else, and a nil channel selects as "never" — exactly the
	// "no operation in flight, nothing to cancel" semantics, with no fallback
	// needed. Guarded by cancelSignalMu because the scheduler bridge inspects
	// this instance from another goroutine while Run swaps the signal.
	cancelSignal   <-chan struct{}
	cancelSignalMu sync.RWMutex

	// Skill hints captured at the start of Run() from auto-activated or
	// manually invoked skills. Applied to each LLM turn via ctx for the
	// duration of the agent loop. Cleared when the loop exits.
	skillModelHint  string
	skillEffortHint llmclient.SkillEffort

	// commandToolScope is the allowed-tools overlay staged by a slash
	// command's frontmatter for the run it initiated. Non-empty scope: a
	// tool call outside the list escalates allow→ask at the security gate
	// (never a silent deny — the human arbitrates, and unattended surfaces
	// resolve it through the existing policy path). Empty: no effect.
	commandToolScope []string

	// injectedSkillNames tracks every skill already delivered to the model
	// during the current Run() — via the startup system-prompt blocks
	// (pinned/auto/manual) or a mid-loop re-scan injection — so the per-turn
	// re-scan (skill_rescan.go) never injects the same skill twice. Reset at
	// the start of each Run() alongside the skill hints.
	injectedSkillNames map[string]bool

	// skillCollapseTurn maps a skill name to the loop turn at which skill
	// aging collapsed its injection block (releaseCollapsedSkills). Used as
	// a re-injection cooldown by rescanSkillsMidLoop. Reset per Run().
	skillCollapseTurn map[string]int

	// skillCharsInjected accumulates the characters of skill guidance this
	// Run() has injected (startup blocks + mid-loop injections). Once it
	// crosses skillRunBudget, later mid-loop activations degrade their
	// bodies to read-on-demand pointers. Reset per Run().
	skillCharsInjected int

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

	// events is the structured event sink for the CURRENT run, resolved from
	// cli.agentEventSink at the top of processAIResponseAndAct. Nil for every
	// interactive/gateway/scheduler run — all emit helpers are no-ops then,
	// keeping those paths byte-identical. eventToolSeq mints run-unique tool
	// call ids for the sink channel.
	events       agentevents.Sink
	eventToolSeq int
}

// splitStdinChunk consumes raw bytes from a stdin Read() call and returns
// the complete lines found (each terminated with a trailing '\n'). Bytes
// that don't yet form a full line are appended to lineBuf for the next
// chunk to finish.
//
// Both '\n' and '\r' end a line. Cooked TTYs deliver '\n' (ICRNL converts
// the user's CR), but a raw-mode TTY — or one left in a transient state
// after a TIOCSTI inject for park auto-resume — delivers '\r'. Recognizing
// both keeps the security prompt responsive in either mode. CRLF pairs are
// collapsed into a single line (the trailing '\n' is consumed).
func splitStdinChunk(chunk []byte, lineBuf *strings.Builder) []string {
	var lines []string
	for i := 0; i < len(chunk); i++ {
		b := chunk[i]
		if b == 0x08 || b == 0x7f {
			// Backspace/DEL: in cbreak mode the kernel no longer edits the
			// line for us, so the reader must — drop the last rune of the
			// pending partial (rune-safe: multi-byte UTF-8 input is normal
			// in pt-BR). Without this, "abc<BS>d" would submit "abc\x7fd".
			if s := lineBuf.String(); s != "" {
				_, size := utf8.DecodeLastRuneInString(s)
				lineBuf.Reset()
				lineBuf.WriteString(s[:len(s)-size])
			}
			continue
		}
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
func (a *AgentMode) startStdinReader(ctx context.Context) {
	a.stdinMu.Lock()
	defer a.stdinMu.Unlock()
	a.stdinDepth++
	if a.stdinLines != nil {
		// Nested processAIResponseAndAct entry: the outer scope's reader is
		// already draining stdin — spawning a second goroutine would make
		// both race for the same fd (byte interleaving).
		return
	}
	a.spawnStdinReaderLocked(ctx)
}

// spawnStdinReaderLocked starts the reader goroutine. Caller holds stdinMu.
// ctx flows to mid-run side command execution (never to the read loop
// itself — shutdown is via the done channel).
func (a *AgentMode) spawnStdinReaderLocked(ctx context.Context) {
	a.stdinLines = make(chan string, 10)
	a.stdinDone = make(chan struct{})
	a.stdinCancel = newStdinReadCanceler()
	// Own the line editing while the loop runs: without cbreak the kernel
	// echoes typed characters on top of the spinner (where the repaint
	// eats them) and only delivers the line on Enter.
	a.stdinCbreakRestore = enableStdinCbreak()
	a.stdinWg.Add(1)

	// The goroutine works on local copies of the lifecycle values: after a
	// teardown that timed out waiting (blocked console read), the struct
	// fields are re-assigned by the next spawn while this goroutine may
	// still be draining — it must keep talking to ITS channels, not the
	// successor's.
	linesCh, doneCh, canceler := a.stdinLines, a.stdinDone, a.stdinCancel

	go func() {
		defer a.stdinWg.Done()
		// Pin to an OS thread and publish its handle so stopStdinReader can
		// abort a blocking console read via CancelSynchronousIo (Windows;
		// no-op elsewhere).
		canceler.bind()
		defer canceler.unbind()
		var lineBuf strings.Builder
		buf := make([]byte, 512)
		for {
			select {
			case <-doneCh:
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
			a.setTypeaheadPreview(lineBuf.String())
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

				// Side commands (/agents, /board, /mail, /jobs) are handled
				// out-of-band: they must never reach the stdinLines channel,
				// where a security prompt would read "/board" as a denial
				// and the type-ahead drain would hand it to the LLM as text.
				if isSideCommand(line) {
					a.onSideCommand(ctx, line)
					continue
				}

				select {
				case <-doneCh:
					return
				case linesCh <- line:
				}
			}
		}
	}()
}

// Reader shutdown pacing: the clean-exit window covers one full poll cycle
// with slack; the cancel-retry window gives each CancelSynchronousIo attempt
// time to land before retrying (retries close the race where the blocking
// read starts right after a cancel that found no I/O in flight).
const (
	stdinStopGrace       = 150 * time.Millisecond
	stdinCancelRetryWait = 100 * time.Millisecond
	stdinCancelRetries   = 4
)

// stopStdinReader signals the stdin reader goroutine to stop and waits for it
// to exit. On Unix (Linux/macOS), the goroutine exits within ~50ms (one poll
// cycle). On Windows it may be inside a blocking os.Stdin.Read (console reads
// have no deadline); left orphaned it would steal the next prompt's
// keystrokes (no echo until Enter), so the pending read is aborted with
// CancelSynchronousIo on the reader's thread — the documented mechanism for
// cancelling synchronous console I/O.
func (a *AgentMode) stopStdinReader() {
	a.stdinMu.Lock()
	defer a.stdinMu.Unlock()
	if a.stdinDepth > 0 {
		a.stdinDepth--
	}
	if a.stdinDepth > 0 {
		// An outer processAIResponseAndAct scope still owns the reader.
		return
	}
	a.teardownStdinReaderLocked()
}

// teardownStdinReaderLocked stops the goroutine and clears the lifecycle
// fields. Caller holds stdinMu.
func (a *AgentMode) teardownStdinReaderLocked() {
	if a.stdinDone != nil {
		close(a.stdinDone)

		done := make(chan struct{})
		go func() {
			a.stdinWg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Clean exit within one poll cycle.
		case <-time.After(stdinStopGrace):
			a.abortBlockedStdinRead(done)
		}

		a.stdinLines = nil
		a.stdinDone = nil
	}
	if a.stdinCbreakRestore != nil {
		a.stdinCbreakRestore()
		a.stdinCbreakRestore = nil
	}
	a.setTypeaheadPreview("")
}

// suspendStdinReader fully releases the stdin fd regardless of the refcount
// so an interactive program (go-prompt line editing, a Bubble Tea overlay)
// can own the terminal. Pair with resumeStdinReader.
func (a *AgentMode) suspendStdinReader() {
	a.stdinMu.Lock()
	defer a.stdinMu.Unlock()
	a.teardownStdinReaderLocked()
}

// resumeStdinReader restarts the reader after a suspend, but only when some
// loop scope still holds a start reference — resuming after the last stop
// would leak a reader into the REPL prompt.
func (a *AgentMode) resumeStdinReader(ctx context.Context) {
	a.stdinMu.Lock()
	defer a.stdinMu.Unlock()
	if a.stdinDepth > 0 && a.stdinLines == nil {
		a.spawnStdinReaderLocked(ctx)
	}
}

// abortBlockedStdinRead unblocks a reader goroutine parked in a blocking
// console read. On platforms without cancellable synchronous I/O it just
// waits out the poll cycle; if every attempt fails the goroutine degrades to
// the old behavior (discards data and exits on the next stdin input).
func (a *AgentMode) abortBlockedStdinRead(done <-chan struct{}) {
	for attempt := 0; attempt < stdinCancelRetries; attempt++ {
		if !a.stdinCancel.cancel() {
			// No cancellation on this platform; one bounded wait.
			select {
			case <-done:
			case <-time.After(stdinCancelRetryWait * stdinCancelRetries):
			}
			return
		}
		select {
		case <-done:
			return
		case <-time.After(stdinCancelRetryWait):
		}
	}
}

// withInteractiveStdin runs fn while the terminal is exclusively owned by it
// (e.g. a Bubble Tea overlay). The centralized stdin reader goroutine drains
// os.Stdin continuously, which would steal keystrokes from a raw-mode program,
// so we stop it for the duration and restart it afterwards. The cooked-mode
// state is snapshotted and restored around fn — belt-and-suspenders against a
// program that leaves the TTY in raw mode on a dirty exit (same rationale as
// runWithCookedTerminalRestore).
//
// @ask is interactive precisely because the loop paused to ask the user, so the
// reader is idle here and no type-ahead is lost.
func (a *AgentMode) withInteractiveStdin(ctx context.Context, fn func() error) error {
	fd := int(os.Stdin.Fd())
	a.suspendStdinReader()
	state, _ := term.GetState(fd)
	err := fn()
	if state != nil {
		_ = term.Restore(fd, state)
	}
	a.resumeStdinReader(ctx)
	return err
}

// reapplyStdinCbreak re-arms the cbreak TTY state after an interactive
// consumer (security prompt, command-block menu) deliberately restored
// cooked mode via stty sane. Cheap (one stty exec) and idempotent; no-op
// when no reader is active.
func (a *AgentMode) reapplyStdinCbreak() {
	a.stdinMu.Lock()
	defer a.stdinMu.Unlock()
	if a.stdinLines == nil {
		return
	}
	if a.stdinCbreakRestore != nil {
		a.stdinCbreakRestore()
	}
	a.stdinCbreakRestore = enableStdinCbreak()
}

// handleAgentAsk drives the @ask / ask_user tool: it parses the question spec,
// renders the interactive overlay (pausing the stdin reader), and returns the
// user's selections as the tool result string. In unattended/non-TTY contexts
// it returns the non-interactive fallback so the daemon/gateway never blocks on
// stdin that will never arrive.
func (a *AgentMode) handleAgentAsk(ctx context.Context, argsJSON string) (string, error) {
	qs, err := ask.ParseRequest(argsJSON)
	if err != nil {
		return ask.ErrorResult(err), err
	}

	// No interactive terminal: gateway/daemon (unattended) or piped one-shot.
	if (a.cli != nil && a.cli.unattended) || !term.IsTerminal(int(os.Stdin.Fd())) {
		return ask.FallbackResult(qs), nil
	}

	var answers []ask.Answer
	var canceled bool
	runErr := a.withInteractiveStdin(ctx, func() error {
		var e error
		answers, canceled, e = palette.RunAsk(ctx, palette.NewAsk(qs))
		return e
	})
	if runErr != nil {
		return ask.ErrorResult(runErr), runErr
	}
	if canceled {
		return ask.CanceledResult(), nil
	}
	return ask.FormatResult(answers), nil
}

// dangerBlocked reports whether a dangerous command must be declined rather
// than auto-approved: only in unattended mode with the block policy set (MCP
// server default-off opt-in via CHATCLI_MCP_DANGER=block). Attended runs keep
// the interactive confirmation; the gateway daemon keeps auto-approve.
func (a *AgentMode) dangerBlocked() bool {
	return a.cli != nil && a.cli.unattended && a.cli.dangerBlock
}

// unattendedConfirmAnswer is what readLine returns in unattended mode (the
// gateway daemon). It is the explicit phrase the dangerous-command guard in
// executeCommandsWithOutput expects, so confirmations auto-approve without any
// human or stdin. It also starts with "s", satisfying the lighter [s/y] prompts.
const unattendedConfirmAnswer = "sim, quero executar conscientemente"

// setCancelSignal publishes the active loop's cancellation channel so blocking
// stdin reads abort when the operation is interrupted. Pass nil to detach it
// when the loop exits; a nil channel selects as "never", so reads simply block
// on input again — the correct "nothing in flight to cancel" behavior.
func (a *AgentMode) setCancelSignal(done <-chan struct{}) {
	a.cancelSignalMu.Lock()
	a.cancelSignal = done
	a.cancelSignalMu.Unlock()
}

// currentCancelSignal returns the active loop's cancellation channel, or nil
// when no Run is executing (e.g. unit tests that call readLine directly).
func (a *AgentMode) currentCancelSignal() <-chan struct{} {
	a.cancelSignalMu.RLock()
	defer a.cancelSignalMu.RUnlock()
	return a.cancelSignal
}

// readLine reads a single line from the centralized stdin reader, aborting on
// cancellation of the active operation (Ctrl+C). See readLineSignal.
func (a *AgentMode) readLine() string {
	return a.readLineSignal(a.currentCancelSignal())
}

// readLineSignal reads a single line from the centralized stdin reader and
// returns it, or returns "" the moment the cancel channel fires. Returning ""
// on cancellation is the safe default for every interactive prompt that funnels
// through here: an empty answer never matches an affirmative confirmation
// phrase, so a Ctrl+C at a dangerous-command guard declines the action instead
// of hanging on the channel receive until the user presses Enter — the hang
// that left the terminal and reader in a broken state for the next REPL turn.
//
// A nil cancel channel never fires (select treats it as "never"), so callers
// with no operation in flight simply block on input as before.
//
// In unattended mode there is no human/stdin, so every confirmation
// auto-approves. When the centralized reader is inactive (tests, non-TTY) it
// falls back to a direct blocking read; that path has no concurrent operation
// to cancel, so it is intentionally not cancellation-aware.
func (a *AgentMode) readLineSignal(cancel <-chan struct{}) string {
	if a.cli != nil && a.cli.unattended {
		return unattendedConfirmAnswer
	}
	if a.stdinLines != nil {
		select {
		case line := <-a.stdinLines:
			return line
		case <-cancel:
			return ""
		}
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
	// Same Windows input sanitation as the main REPL (see cli.go): AltGr
	// chars arrive as ESC+rune and would land as a stray glyph without the
	// altGrParser wrapper.
	var inputParser prompt.ConsoleParser = prompt.NewStandardInputParser()
	if runtime.GOOS == "windows" {
		inputParser = newAltGrParser(inputParser)
	}
	pasteParser := paste.NewBracketedPasteParser(
		inputParser,
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

// drainStdinToQueue collects every pending stdin line into ONE newline-joined
// user message for injection at the turn boundary. Historically only the
// first line was injected and the rest were pushed onto cli.messageQueue —
// but agent mode never dequeues that queue (its sole consumer is the
// chat-mode lifecycle), so lines 2..N were stranded until the user returned
// to chat. Folding them keeps every typed instruction in the turn it was
// meant for.
func (a *AgentMode) drainStdinToQueue() string {
	var lines []string
	for {
		select {
		case line := <-a.stdinLines:
			if line == "" {
				continue // skip empty lines (bare Enter presses)
			}
			// Swallow scheduler-injected "/resume <token>" lines whose
			// token is already queued for auto-resume (the bridge may
			// have injected while this loop was starting, before the
			// isExecuting guard flipped). The queued resume still fires
			// via drainPendingResumes once this loop exits — the line
			// itself must not reach the LLM as a user instruction.
			if token, ok := parsePendingResumeLine(line); ok && a.cli.hasPendingResume(token) {
				fmt.Println(colorize(" ⏸ "+i18n.T("park.resume.queued_while_busy", token), ColorCyan))
				continue
			}
			lines = append(lines, line)
		default:
			return strings.Join(lines, "\n")
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
	line := a.readLine()
	// This helper forced cooked mode above for readable menu input; re-arm
	// cbreak so the live type-ahead preview keeps working afterwards.
	a.reapplyStdinCbreak()
	return line
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
//
// Priority for the turn's model: the AI's own "@model use" route override
// (an explicit in-task decision) wins over the skill frontmatter hint (a
// static preference captured at Run() start). The override handle is always
// the qualified "PROVIDER:model" form, so its resolution is deterministic.
func (a *AgentMode) clientAndCtxForTurn(ctx context.Context) (llmclient.LLMClient, context.Context) {
	turnClient := a.cli.Client
	hint := a.cli.agentRouteOverrideHandle()
	if hint == "" {
		hint = a.skillModelHint
	}
	if hint != "" {
		resolution := a.cli.resolveSkillClient(hint)
		turnClient = resolution.Client
		// Log once per turn — the ReAct loop can spin many times and we
		// don't want to spam. agent_mode.go only calls this helper from
		// the turn loop, so a single log per turn is acceptable.
		if resolution.Changed {
			a.logger.Debug("agent turn: model routing hint honored",
				zap.String("hint", hint),
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

// effectiveRoute reports the provider and model that actually serve the
// current turn: the AI's @model route override first, then the skill
// frontmatter hint, then the session's own pair (with the same env/config
// fallbacks initMultiAgent applies when the session provider is unset).
// Every consumer that must follow a mid-task model switch — cost
// attribution, the squad dispatcher, subagent delegation — reads this
// instead of the frozen session fields.
func (a *AgentMode) effectiveRoute() (provider, model string) {
	hint := a.cli.agentRouteOverrideHandle()
	if hint == "" {
		hint = a.skillModelHint
	}
	if hint != "" {
		resolution := a.cli.resolveSkillClient(hint)
		if resolution.Provider != "" && resolution.Model != "" {
			return resolution.Provider, resolution.Model
		}
	}
	provider = a.cli.Provider
	if provider == "" {
		provider = os.Getenv("LLM_PROVIDER")
	}
	if provider == "" {
		provider = config.Global.GetString("LLM_PROVIDER")
	}
	return provider, a.cli.Model
}

// Run inicia o modo agente com uma consulta do usuário, utilizando um loop de Raciocínio-Ação (ReAct).
// Agora aceita systemPromptOverride para definir personas específicas (ex: Coder).
func (a *AgentMode) Run(ctx context.Context, query string, additionalContext string, systemPromptOverride string) error {
	// The journal must hold whatever the loop produced, however it exits.
	defer a.cli.syncTranscript()
	// Reentrancy guard. AgentMode is not safe to Run concurrently on the
	// same instance — see runInflight comment in the struct. We CAS so
	// that any caller stepping on an in-flight Run gets a clean error
	// instead of corrupting shared state (taskTracker, history, TUI).
	if !a.runInflight.CompareAndSwap(false, true) {
		return fmt.Errorf("agent: another Run is already in flight on this AgentMode instance")
	}
	defer a.runInflight.Store(false)

	// Register the orchestrator itself in the process-wide run registry.
	// Workers, subagents and MoA members spawned from this loop inherit the
	// returned ctx and parent to this run — that is what makes the /agents
	// tree view possible. A park suspension counts as a clean end.
	orchCtx, orchRun := a.beginOrchestratorRun(ctx, query, systemPromptOverride)
	ctx = orchCtx

	// CHATCLI_PROMPT_CACHE_TTL=auto: the agent/coder loop prefers the hour
	// (long sessions that pause between tool rounds); chat and one-shot
	// keep the 5-minute default, restored when this run ends.
	llmclient.SetPromptCacheTTLHint("1h")
	defer llmclient.SetPromptCacheTTLHint("5m")
	defer func() {
		orchRun.End(nil)
	}()

	// Cross-surface continuity first (adopt another surface's writes to the
	// active named session), then pull turns that arrived on other channels
	// (Telegram/…) into history so the agent/coder has cross-channel context.
	// Both no-op where they don't apply (captured RPC runs, gateway). Silent.
	a.cli.refreshBoundSession()
	a.cli.syncHubContext(ctx)

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
	coreText := a.composeCoreText(isCoder, hasActivePersona)

	a.isCoderMode = isCoder
	// isOneShot is set by the caller before Run (false for the interactive
	// /agent and /coder entries, true for the one-shot RunCoderOnce / gateway
	// paths). Run no longer resets it: doing so silently clobbered the one-shot
	// intent, leaving coder one-shot to escape only via EOF on the
	// wait-for-input branch — which a stdin-less daemon can't rely on.

	// Block 3 — workspace / retrieval context. Built only when we actually
	// have a context builder; empty string means "skip this block".
	// dynamicText (wall-clock time + cwd disambiguation) is captured
	// SEPARATELY from workspaceText: the timestamp changes every turn, so
	// bundling it into the cacheable workspace block would bust the prefix
	// cache. It is emitted as its own uncached trailing block instead.
	workspaceText, dynamicText := a.buildWorkspaceBlocks(ctx, query)

	// Block 2 — tool descriptions (plugins) + session workspace hint.
	// Merged into one cacheable block since they're always emitted as a pair.
	toolsText := a.getToolContextString() + buildSessionWorkspaceHint()
	// The prefix budget (prompt_budget.go) sizes the unbounded sections —
	// knowledge digests, then attachments — against the window; skills
	// have their own run budget further down.
	promptBudget := a.cli.newPrefixBudget(a.cli.Provider, a.cli.Model)
	promptBudget.spend(len(coreText) + len(toolsText))
	// Attached knowledge bases ride in the same cacheable block: their index
	// cards are deterministic (change only on attach/detach, like the plugin
	// catalog) and they tell the model what the @knowledge tool can reach —
	// the agent-mode counterpart of the chat pipeline's digest injection.
	if kb, folded := a.cli.knowledgeAgentBlockBudgeted(promptBudget.remaining()); kb != "" {
		toolsText += "\n\n" + kb
		promptBudget.spend(len(kb))
		if folded {
			promptBudget.noteDegraded("knowledge")
		}
	}
	// The session's /context attachments ride here too (session-stable,
	// cache-friendly) — the agent-mode counterpart of the chat pipeline's
	// Part 1, so a context attached in chat is visible to /agent, /coder,
	// the gateway and the MCP server alike.
	if cb, folded := a.cli.attachedContextAgentBlockBudgeted(promptBudget.remaining()); cb != "" {
		toolsText += "\n\n" + cb
		promptBudget.spend(len(cb))
		if len(folded) > 0 {
			promptBudget.noteDegraded("attached")
		}
	}
	// Teach the autonomous documentation pipeline so the model proactively
	// builds the knowledge it lacks instead of guessing or stalling. Cheap,
	// deterministic, and rides in the same cacheable block.
	toolsText += "\n\n" + contextPipelineHint()
	// Pilar 1A: nudge proactive in-turn skill authoring/evolution (cacheable,
	// empty when self-evolution is off).
	if sh := skillAuthoringHint(); sh != "" {
		toolsText += "\n\n" + sh
	}

	// Fase 5: Inject auto-activated skills (triggers + path globs) into the
	// agent-mode system prompt. Also honors a `/<skill-name>` manual
	// invocation that was staged right before Run() was called — that is
	// how `/coder` and `/agent` interplay with skill invocation.
	//
	// Only fires once at the start of the loop so we don't inflate cache
	// across tool iterations.
	//
	// Reset hints from any previous Run() so a second `/agent` call
	// without an active skill does not inherit the old model/effort. The
	// @model route override is task-scoped for the same reason: the AI's
	// routing decision must never silently outlive the task it was made for.
	a.skillModelHint = ""
	a.skillEffortHint = llmclient.EffortUnset
	a.cli.clearAgentRouteOverride()
	a.injectedSkillNames = make(map[string]bool)
	a.skillCollapseTurn = make(map[string]int)
	a.skillCharsInjected = 0
	a.lastBoardNudge = ""
	// Slash-command allowed-tools overlay: staged by the expansion that
	// initiated this run (gateway, ACP, one-shot, coder). Consumed here so
	// it is scoped to THIS run and can never leak into the next one.
	a.commandToolScope = a.cli.consumePendingCommandToolScope()
	// Block 4 — skills (pinned + auto-activated + manual) and Orchestrator
	// catalog. Built last because it's the most volatile (changes per query)
	// and sits at the tail of the system prompt so earlier blocks stay
	// cacheable. Pinned skills go before auto-activated so they win on
	// model/effort ties via pickSkillModelAndEffort's "first non-empty wins"
	// rule.
	skillsText := a.buildAgentSkillBlocks(query, additionalContext)
	skillsText = a.applyManualSkillAndCommandHints(skillsText)
	// Seed the per-Run skill budget with everything the startup blocks
	// already spent, so mid-loop injections account against the same pool.
	a.skillCharsInjected = len(skillsText)

	// If we captured a skill model hint, pre-resolve it once so the user
	// sees exactly what will run (or why their preference is being
	// ignored) before the agent loop starts burning turns.
	if a.skillModelHint != "" {
		a.cli.ensureModelCacheWarm(ctx)
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
	if a.initMultiAgent(ctx) {
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
	// SystemParts for Anthropic-style KV cache. buildAgentSystemMessage
	// owns the stable-prefix / volatile-suffix split: only the stable
	// blocks (core/tools/orchestrator) carry ephemeral breakpoints; the
	// volatile blocks (workspace/skills/channels/dynamic) trail uncached.
	// Volatile MCP channel context — most recent push messages from
	// connected servers. Surfaces CI alerts, monitoring events, etc.
	// in agent/coder mode so the agent's plan can react to them.
	// Empty when MCP is disabled or no events have been received.
	var channelsText string
	if a.cli.mcpManager != nil {
		channelsText = a.cli.mcpManager.Channels().FormatForPrompt(5)
	}

	// The scratch dir path is fixed for the process, so it belongs to the
	// cacheable tools block: stable for the session, never in the tail.
	if wsLine := sessionWorkspaceDynamicLine(); wsLine != "" {
		toolsText = strings.TrimRight(toolsText, "\n") + "\n\n" + wsLine
	}

	// The per-turn context (date, proactive recall, channel pushes) never
	// enters the system message: it rides as a flagged user-role message
	// before the query (turn_context.go), so the system prompt stays
	// byte-stable and the prefix cache keeps hitting across runs.
	turnContextText := composeTurnContext(channelsText, dynamicText)
	sysMsg := buildAgentSystemMessage(coreText, toolsText, workspaceText, skillsText, orchestratorText, "", "")
	breakdownMode := "agent"
	if isCoder {
		breakdownMode = "coder"
	}
	a.cli.promptBreakdowns.recordDegraded(breakdownMode, promptBudget.Degraded(), []promptSection{
		{Name: "core", Chars: len(strings.TrimSpace(coreText)), Cached: true},
		{Name: "tools", Chars: len(strings.TrimSpace(toolsText)), Cached: true},
		{Name: "orchestrator", Chars: len(strings.TrimSpace(orchestratorText)), Cached: true},
		{Name: "workspace_memory", Chars: len(strings.TrimSpace(workspaceText))},
		{Name: "skills", Chars: len(strings.TrimSpace(skillsText))},
		{Name: "turn_context", Chars: len(strings.TrimSpace(turnContextText))},
	})

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
	a.installAgentSystemMessage(sysMsg, currentModeName)
	a.toolDefsChars = a.estimateToolDefsChars()

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

	a.appendTurnContext(turnContextText)
	a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: currentQuery, Images: a.pendingUserImages})
	a.pendingUserImages = nil

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
	endRun, err := a.settleParkSentinel(ctx, err)
	if endRun {
		return err
	}
	// Mirror the user request + final answer onto the shared conversation so
	// the turn shows up as context on other channels. No-op when hub sync is
	// off; tool execution detail stays local.
	if err == nil && strings.TrimSpace(a.cli.lastAgentReply) != "" {
		a.cli.mirrorHubTurn(ctx, query, a.cli.lastAgentReply)
	}
	// Write the run through to the active named session (no-op during
	// captured RPC runs — the backend persists per-session there).
	if err == nil {
		a.cli.persistBoundSession()
	}
	// Close the orchestrator's registry entry with the real outcome; the
	// deferred End(nil) then no-ops (End is idempotent, first call wins).
	orchRun.End(err)
	return err
}

// beginOrchestratorRun registers the main loop in the run registry, deriving
// the agent label from the active mode and the origin from the unattended
// flag. Split out of Run to keep its cyclomatic complexity in budget.
func (a *AgentMode) beginOrchestratorRun(ctx context.Context, query, systemPromptOverride string) (context.Context, *runs.Run) {
	orchAgent := "agent"
	if systemPromptOverride == CoderSystemPrompt {
		orchAgent = "coder"
	}
	orchOrigin := "repl"
	if a.cli.unattended {
		orchOrigin = "gateway"
	}
	return runs.Default().Begin(ctx, runs.Info{
		Kind:   runs.KindOrchestrator,
		Agent:  orchAgent,
		Task:   query,
		Origin: orchOrigin,
	})
}

// composeTurnContext joins the per-turn blocks (MCP channel pushes first,
// then date/recall) into the text of the run's turn context message.
func composeTurnContext(channelsText, dynamicText string) string {
	out := strings.TrimSpace(dynamicText)
	if c := strings.TrimSpace(channelsText); c != "" {
		if out == "" {
			return c
		}
		out = c + "\n\n" + out
	}
	return out
}

// appendTurnContext appends the flagged turn context message (no-op when
// empty).
func (a *AgentMode) appendTurnContext(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	a.cli.history = append(a.cli.history, models.TurnContextMessage(turnContextHeader+text))
}

// installAgentSystemMessage purges stale mode system messages and installs the
// freshly-built sysMsg for the current mode — replacing a surviving same-mode
// system message in place, or prepending when none exists.
func (a *AgentMode) installAgentSystemMessage(sysMsg models.Message, currentModeName string) {
	a.cli.history = purgeStaleModeSystems(a.cli.history, currentModeName)

	if len(a.cli.history) == 0 {
		a.cli.history = append(a.cli.history, sysMsg)
		return
	}
	// Replace any surviving system message of the CURRENT mode with
	// the freshly-built sysMsg (workspace/skills/orchestrator blocks
	// may have changed across turns). Otherwise prepend.
	for i, msg := range a.cli.history {
		if msg.Role == "system" && modeOfSystemMessage(msg) == currentModeName {
			a.cli.history[i] = sysMsg
			return
		}
	}
	a.cli.history = append([]models.Message{sysMsg}, a.cli.history...)
}

// composeCoreText builds Block 1 of the agent system prompt (the stable core
// behavior block): persona/coder/default base plus the language and gateway
// directives. Split out of Run to keep its cyclomatic complexity in check.
func (a *AgentMode) composeCoreText(isCoder, hasActivePersona bool) string {
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
		// The gateway answers through a chat app: use the dedicated
		// conversational base (same tools, friendlier voice) instead of the
		// terse coder prompt. Only when no persona owns the core text.
		coreText = coderBaseSystemPrompt(a.gatewayPersona)
	} else {
		osName := runtime.GOOS
		shellName := utils.GetUserShell()
		currentDir, _ := os.Getwd()
		coreText = i18n.T("agent.system_prompt.default.base", osName, shellName, currentDir)
	}
	// Language directive. The interactive CLI pins the daemon/user locale; the
	// gateway must instead MIRROR each incoming message's language (every user
	// writes in their own). Apply the dynamic directive on EVERY gateway path —
	// including when an active persona owns the core text and the
	// GatewaySystemPrompt (which also says this) isn't used — so the reply is
	// never statically pinned to one language.
	if a.gatewayPersona {
		coreText += "\n\n" + GatewayLanguageDirective
		// The daemon answers strangers' questions about the user unless told
		// the injected Memory Index is real knowledge — applied on every
		// gateway path, including persona-owned core text.
		coreText += "\n\n" + GatewayMemoryDirective
	} else {
		coreText += "\n\n" + i18n.T("ai.response_language")
	}

	// Gateway persona reinforcement: only needed when the core text is NOT
	// already the dedicated GatewaySystemPrompt — i.e. when an active persona
	// (or a non-coder profile) owns the core block. Steers those toward concise,
	// plain-text chat replies. The GatewaySystemPrompt path already embeds this.
	if a.gatewayPersona && (hasActivePersona || !isCoder) {
		coreText += "\n\n" + i18n.T("gateway.persona_prompt")
	}
	// Output-token reduction: append the (static, cache-friendly) verbosity
	// directive so the model drops preamble/restatement/ceremony. Empty when
	// CHATCLI_OUTPUT_VERBOSITY=full.
	coreText += verbosityDirectiveBlock()
	// Persistent-memory bootstrap card: counts snapshot + navigation routes
	// (@memory/@session/@board). Session-start stable, so it belongs in this
	// CACHED core block. The gateway keeps its own GatewayMemoryDirective on
	// top — the card adds the live counts and the saved-sessions route that
	// directive predates.
	if card := a.cli.memoryBootstrapCardAgent(); card != "" {
		coreText += "\n\n" + card
	}
	return coreText
}

// buildWorkspaceBlocks builds Block 3 of the agent system prompt: the
// workspace/retrieval context plus the separately-cached dynamic (wall-clock)
// context. Returns empty strings when no context builder is configured.
func (a *AgentMode) buildWorkspaceBlocks(ctx context.Context, query string) (string, string) {
	if a.cli.contextBuilder == nil {
		return "", ""
	}
	var hints []string
	hintWindow := 3
	if len(a.cli.history) < hintWindow {
		hintWindow = len(a.cli.history)
	}
	var recentTexts []string
	if hintWindow > 0 {
		for _, msg := range a.cli.history[len(a.cli.history)-hintWindow:] {
			recentTexts = append(recentTexts, msg.Content)
		}
	}
	// The current query is appended to history only AFTER the system prompt
	// is assembled, so it must feed the hints explicitly — otherwise the
	// first turn of a fresh session (the moment recall matters most) runs
	// hintless and every proactive recall block stays mute.
	if q := strings.TrimSpace(query); q != "" {
		recentTexts = append(recentTexts, q)
	}
	if len(recentTexts) > 0 {
		hints = memory.ExtractKeywords(recentTexts)
	}
	// Memory injection mode: "index" (pull) keeps the per-turn payload
	// bounded — a small stable digest plus the recall directive — while
	// "full" (push) injects the whole hint-driven retrieval. Agent/coder
	// can pull, so they honor the configured mode directly.
	mode := loadMemoryMode()
	aug := a.cli.hydeAugmenterFor(a.qualityConfig)
	recallHint := ""
	if mode == memModeIndex {
		recallHint = memoryRecallHint
	}
	workspaceText := a.cli.contextBuilder.BuildWorkspaceContextMode(ctx, query, hints, aug, mode, recallHint)

	// Append the knowledge-graph map-of-content card next to the memory index.
	// It is tiny and deterministic (so prompt-cache friendly), and agent/coder
	// can pull a subject's neighborhood on demand via @graph.
	if mode != memModeOff {
		if gb := a.cli.graphIndexBlock(); gb != "" {
			if strings.TrimSpace(workspaceText) == "" {
				workspaceText = gb
			} else {
				workspaceText = strings.TrimRight(workspaceText, "\n") + "\n\n" + gb
			}
		}
	}

	dynamicText := a.cli.contextBuilder.BuildDynamicContext()
	// Proactive recall (index mode only): the top hint-matching facts ride in
	// the UNCACHED trailing block with the wall-clock context — hint-driven
	// text changes every turn, and placing it in the stable workspace block
	// would poison the prompt cache for everything after it.
	if mode == memModeIndex {
		if ar := a.cli.memoryAutoRecallBlockCtx(ctx, hints, query); ar != "" {
			if dynamicText == "" {
				dynamicText = ar
			} else {
				dynamicText = ar + "\n\n" + dynamicText
			}
		}
	}
	// Proactive SESSION recall (own gate, orthogonal to the memory mode —
	// saved sessions are a separate layer neither "index" nor "full"
	// injects). Same cache discipline: uncached trailing block only.
	// The CURRENT query is the referential text — lastUserMessage(history)
	// here would test the referential regex against the PREVIOUS turn (or a
	// synthetic tool-feedback message), so "lembra do que fizemos ontem?" as
	// the opening /coder query never fired.
	refText := strings.TrimSpace(query)
	if refText == "" {
		refText = lastUserMessage(a.cli.history)
	}
	if sr := a.cli.sessionAutoRecallBlock(hints, refText); sr != "" {
		if dynamicText == "" {
			dynamicText = sr
		} else {
			dynamicText = sr + "\n\n" + dynamicText
		}
	}
	return workspaceText, dynamicText
}

// applyManualSkillAndCommandHints consumes the pending manual-skill
// invocation and the slash-command hints staged for this run: the manual
// skill's block is appended to skillsText, and model/effort hints land on
// the run (command hints outrank the skill's — the command invocation is
// the more explicit user intent). Extracted from Run for cyclomatic budget.
func (a *AgentMode) applyManualSkillAndCommandHints(skillsText string) string {
	if a.cli.pendingManualSkill != nil {
		manual := a.cli.pendingManualSkill
		manualArgs := a.cli.pendingManualSkillArgs
		a.cli.pendingManualSkill = nil
		a.cli.pendingManualSkillArgs = ""
		a.noteInjectedSkills(manual)
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
	if m, e := a.cli.consumePendingCommandHints(); m != "" || e != "" {
		if m != "" {
			a.skillModelHint = m
		}
		if e != "" {
			a.skillEffortHint = llmclient.NormalizeEffort(e)
		}
	}
	return skillsText
}

// commandScopeAllows reports whether toolName fits the active slash-command
// allowed-tools overlay. Matching is by bare tool name, tolerant of the "@"
// prefix on either side ("read" matches "@read"). Empty scope = no
// restriction.
func (a *AgentMode) commandScopeAllows(toolName string) bool {
	if len(a.commandToolScope) == 0 {
		return true
	}
	name := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(toolName)), "@")
	for _, allowed := range a.commandToolScope {
		if strings.TrimPrefix(strings.ToLower(strings.TrimSpace(allowed)), "@") == name {
			return true
		}
	}
	return false
}

// stageViewedImage queues an image the @view tool attached mid-run for the
// next turn boundary.
func (a *AgentMode) stageViewedImage(img models.ImageContent, name string) {
	a.stagedViewMu.Lock()
	defer a.stagedViewMu.Unlock()
	a.stagedViewImages = append(a.stagedViewImages, img)
	a.stagedViewNames = append(a.stagedViewNames, name)
}

// drainViewedImages returns and clears the staged @view attachments.
func (a *AgentMode) drainViewedImages() ([]models.ImageContent, []string) {
	a.stagedViewMu.Lock()
	defer a.stagedViewMu.Unlock()
	imgs, names := a.stagedViewImages, a.stagedViewNames
	a.stagedViewImages, a.stagedViewNames = nil, nil
	return imgs, names
}

// expandFollowUpCommand resolves a mid-run user follow-up against the slash
// command catalog. Mid-run semantics differ from a turn-initiating
// expansion: the provider/model cannot change mid-loop, so model/effort
// hints are consumed and dropped; the allowed-tools overlay however
// re-arms for the remainder of the run — the user just narrowed the scope
// on purpose.
func (a *AgentMode) expandFollowUpCommand(ctx context.Context, text string) string {
	if !strings.HasPrefix(text, "/") {
		return text
	}
	expanded, ok := a.cli.expandSlashCommandInput(ctx, text, !a.cli.unattended)
	if !ok {
		return text
	}
	a.cli.consumePendingCommandHints() // unusable mid-run; never leak into the next Run
	if scope := a.cli.consumePendingCommandToolScope(); len(scope) > 0 {
		a.commandToolScope = scope
	}
	return expanded
}

// followUpRecallBlocks re-runs the proactive recall surfaces for a mid-loop
// user follow-up. The agent system prompt is assembled ONCE per Run, so the
// boot-time [SESSION RECALL] / [MEMORY AUTO-RECALL] blocks freeze at their
// turn-one state — a follow-up like "lembra do que fizemos ontem?" typed
// mid-session would otherwise never trigger recall. Returns "" when nothing
// matched. The caller injects the result append-only as a user-role message
// at the turn boundary, the same protocol-safety contract as the mid-loop
// skill blocks (never between a tool_use and its tool_result).
func (a *AgentMode) followUpRecallBlocks(ctx context.Context, userMsg string) string {
	userMsg = strings.TrimSpace(userMsg)
	if userMsg == "" {
		return ""
	}
	hints := memory.ExtractKeywords([]string{userMsg})
	var parts []string
	if loadMemoryMode() == memModeIndex {
		// Bounded so the (optional) embedding call can never stall a turn.
		recallCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		ar := a.cli.memoryAutoRecallBlockCtx(recallCtx, hints, userMsg)
		cancel()
		if ar != "" {
			parts = append(parts, ar)
		}
	}
	if sr := a.cli.sessionAutoRecallBlock(hints, userMsg); sr != "" {
		parts = append(parts, sr)
	}
	return strings.Join(parts, "\n\n")
}

// RunCoderOnce executa o modo coder de forma não-interativa (one-shot),
// mas mantendo o loop ReAct do AgentMode (com tool_calls/plugins).
func (cli *ChatCLI) RunCoderOnce(ctx context.Context, input string) error {
	query, ok := strings.CutPrefix(input, "/coder ")
	if !ok {
		return fmt.Errorf("entrada inválida para o modo coder one-shot: %s", input)
	}
	return cli.runCoderQuery(ctx, query, false)
}

// RunGatewayCoderOnce runs the coder ReAct loop one-shot on a raw task for the
// messaging gateway: same engine and tools (create/edit files, run commands,
// iterate) but with the gateway persona layered on the system prompt so the
// answer is concise, chat-friendly prose. The caller is expected to have set
// cli.unattended so every confirmation auto-approves — the gateway must never
// block on a stdin prompt the daemon has no way to answer.
func (cli *ChatCLI) RunGatewayCoderOnce(ctx context.Context, task string) error {
	return cli.runCoderQuery(ctx, task, true)
}

// RunAgentFullOnce runs the FULL agent ReAct loop one-shot on a raw task —
// the same engine, tools, skills and workspace context the interactive /agent
// command gets — exiting when the loop reaches its final answer. This is the
// agent-profile mirror of runCoderQuery, and the entry the MCP server's
// agent_task tool uses; the legacy single-call RunOnce path remains only for
// the CLI one-shot (-p "/agent …") whose auto-exec contract predates the loop.
func (cli *ChatCLI) RunAgentFullOnce(ctx context.Context, task string) error {
	cli.setExecutionProfile(ProfileAgent)
	defer cli.setExecutionProfile(ProfileNormal)

	// Processar contextos especiais como @file, @git, etc.
	query, additionalContext, images := cli.processSpecialCommands(ctx, task)
	if len(cli.pendingInboundImages) > 0 {
		images = append(images, cli.pendingInboundImages...)
		cli.pendingInboundImages = nil
	}
	images, visionDesc := cli.gateImagesForModel(ctx, images)
	additionalContext += visionDesc
	fullQuery := query
	if additionalContext != "" {
		fullQuery = query + "\n\nContexto adicional:\n" + additionalContext
	}

	if cli.agentMode == nil {
		cli.agentMode = NewAgentMode(cli, cli.logger)
	}

	cli.agentMode.pendingUserImages = images
	cli.agentMode.isCoderMode = false
	cli.agentMode.isOneShot = true
	return cli.agentMode.Run(ctx, fullQuery, "", "")
}

// runCoderQuery is the shared body for the coder one-shot entries. It runs the
// AgentMode ReAct loop under the coder profile and system prompt; gatewayPersona
// toggles the messaging-gateway directive (see AgentMode.gatewayPersona).
func (cli *ChatCLI) runCoderQuery(ctx context.Context, query string, gatewayPersona bool) error {
	cli.setExecutionProfile(ProfileCoder)
	defer cli.setExecutionProfile(ProfileNormal)

	// Processar contextos especiais como @file, @git, etc.
	query, additionalContext, images := cli.processSpecialCommands(ctx, query)
	// Merge images staged from outside the @file flow (e.g. the gateway), then
	// gate against the active model (native vision vs describe-fallback).
	if len(cli.pendingInboundImages) > 0 {
		images = append(images, cli.pendingInboundImages...)
		cli.pendingInboundImages = nil
	}
	images, visionDesc := cli.gateImagesForModel(ctx, images)
	additionalContext += visionDesc
	fullQuery := query
	if additionalContext != "" {
		fullQuery = query + "\n\nContexto adicional:\n" + additionalContext
	}

	// Assegurar que o modo agente está inicializado
	if cli.agentMode == nil {
		cli.agentMode = NewAgentMode(cli, cli.logger)
	}

	cli.agentMode.pendingUserImages = images
	cli.agentMode.isCoderMode = true
	cli.agentMode.isOneShot = true
	cli.agentMode.gatewayPersona = gatewayPersona
	defer func() { cli.agentMode.gatewayPersona = false }()

	// Executa o AgentMode no "perfil coder" (system prompt override):
	// timeline, tool_calls e execução automática de plugins.
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
	a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: enrichedQuery, Images: a.pendingUserImages})
	a.pendingUserImages = nil

	a.cli.animation.ShowThinkingAnimation(a.cli.Client.GetModelName())

	// Session /max-tokens override (0 = provider default), same as the loop.
	legacyMaxTokens := a.cli.UserMaxTokens
	aiResponse, err := a.cli.Client.SendPrompt(ctx, enrichedQuery, a.cli.history, legacyMaxTokens)
	// Auto-retry on OAuth token expiration (401)
	if a.cli.refreshClientOnAuthError(err) {
		aiResponse, err = a.cli.Client.SendPrompt(ctx, enrichedQuery, a.cli.history, legacyMaxTokens)
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
	if a.cli.unattended {
		// No terminal to paint to; capture the clean prose so the gateway can
		// deliver it as the final answer, and keep stdout to the action feed.
		a.cli.lastAgentReply = stripCommandBlocksText(aiResponse, commandBlocks)
		a.cli.reinforceRecalledFacts(a.cli.lastAgentReply)
	} else {
		a.displayResponseWithoutCommands(aiResponse, commandBlocks)
	}

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

	// Unattended runs (gateway daemon, full-autonomy) skip the danger gate —
	// the operator opted in and access is controlled at the gateway edge.
	// The MCP server can re-arm it via CHATCLI_MCP_DANGER=block.
	if !a.cli.unattended || a.dangerBlocked() {
		for _, cmd := range blockToExecute.Commands {
			if a.validator.IsDangerous(cmd) {
				errMsg := i18n.T("agent.oneshot.auto_exec_aborted", cmd)
				fmt.Printf("⚠️ %s\n", errMsg)
				return errors.New(errMsg)
			}
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

// executeBlockRe matches a whole ```execute:<lang> … ``` fence. It mirrors the
// extraction regex in extractCommandBlocks so stripping can't drift from what
// was parsed — rebuilding the literal from the (trimmed) commands missed the
// newline before the closing fence, leaving raw blocks in the gateway reply.
var executeBlockRe = regexp.MustCompile("(?s)```execute:\\s*[a-zA-Z0-9_-]+\\s*\n.*?```")

// stripCommandBlocksText returns the model response with its ```execute blocks
// replaced by compact [Command #N] placeholders — the clean prose delivered as
// the unattended (gateway) final answer. Mirrors displayResponseWithoutCommands.
// Replacing by regex (not by reconstructing each block's literal text) ensures
// every fence is removed regardless of internal whitespace.
func stripCommandBlocksText(response string, blocks []CommandBlock) string {
	n := 0
	out := executeBlockRe.ReplaceAllStringFunc(response, func(string) string {
		desc := ""
		if n < len(blocks) {
			desc = blocks[n].Description
		}
		n++
		return fmt.Sprintf("\n[Command #%d: %s]\n", n, desc)
	})
	return strings.TrimSpace(out)
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

	// Deferred catalog (default): only the CORE work-loop tools carry their
	// full definition inline; every other builtin becomes a one-line index
	// entry the model expands on demand via @tools describe. Measured saving:
	// ~11k → ~2.5k tokens of tool definitions per agent turn. External
	// (user-installed) plugins stay fully rendered — few and explicitly
	// chosen. CHATCLI_AGENT_TOOL_CATALOG=full restores the legacy behavior.
	deferred := toolCatalogDeferred()
	toolDescriptions := make([]string, 0, len(plugins))
	indexLines := make([]string, 0, len(plugins))
	coderCheatSheet := ""
	for _, plugin := range plugins {
		if deferred && plugin.Path() == "" && !isCoreTool(plugin.Name()) {
			indexLines = append(indexLines, renderToolIndexLine(plugin))
			continue
		}
		toolDescriptions = append(toolDescriptions, renderToolBlock(plugin, a.isCoderMode))
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
		// Independently of tool availability, surface any server blocked on
		// OAuth authorization so the model knows it can run @mcp-login to
		// unlock it (its tools only appear after a successful login).
		if note := buildMCPAuthNote(a.cli.mcpManager.GetServerStatus()); note != "" {
			toolDescriptions = append(toolDescriptions, note)
		}
	}

	indexSection := ""
	if len(indexLines) > 0 {
		indexSection = "\n\n" + deferredCatalogInstruction + strings.Join(indexLines, "")
	}
	toolContext := "\n\n" + i18n.T("agent.system_prompt.tools_header") + "\n" + coderCheatSheet + strings.Join(toolDescriptions, "\n") + indexSection + "\n\n" + i18n.T("agent.system_prompt.tools_instruction")
	if a.isCoderMode {
		toolContext += "\nDicas rápidas (@coder):\n" +
			"- Use args JSON sempre que possível: {\"cmd\":\"read\",\"args\":{\"file\":\"main.go\"}}\n" +
			"- Subcomando obrigatório: use \"cmd\" ou \"argv\".\n" +
			"- Para exec, use \"cmd\" (ou \"command\") dentro de args.\n"
	}
	return toolContext
}

// adoptSessionMaxTokens reconciles the agent loop's per-turn max-tokens with
// the session /max-tokens override (cli.UserMaxTokens). The override is
// adopted whenever the user changes it — raising or lowering — but an
// unchanged override never clobbers a value the truncation-recovery
// escalation raised in the meantime. Returns the new (current, lastAdopted)
// pair.
func adoptSessionMaxTokens(current, lastAdopted, sessionOverride int) (int, int) {
	if sessionOverride > 0 && sessionOverride != lastAdopted {
		return sessionOverride, sessionOverride
	}
	return current, lastAdopted
}

// processAIResponseAndAct is the ReAct main loop. It is deliberately
// large — it interleaves stdin draining, LLM streaming, tool parsing,
// policy enforcement, compaction, and UI rendering — and a targeted
// refactor is scoped as its own effort outside the seven-pattern PR.
//
//nolint:gocyclo // legacy main loop; split tracked separately.
func (a *AgentMode) processAIResponseAndAct(ctx context.Context, maxTurns int) error {
	// Publish the loop's cancel channel so blocking confirmation reads abort on
	// Ctrl+C instead of hanging until Enter. Save/restore (not clear-to-nil)
	// because this loop is re-entrant — handleCommandBlocks menu actions and
	// park resume call back into it — so an inner turn must hand the outer
	// turn's signal back when it returns, never leave a later read unguarded.
	prevCancel := a.currentCancelSignal()
	a.setCancelSignal(ctx.Done())
	defer a.setCancelSignal(prevCancel)

	// Start centralized stdin reader for type-ahead queue support.
	// NEVER on unattended surfaces: on the ACP/MCP stdio servers os.Stdin
	// IS the JSON-RPC channel, and this reader would race the protocol
	// scanner for bytes — swallowing the client's permission answers and
	// cancel frames, then feeding the stolen JSON to the LLM as bogus user
	// type-ahead. Same quarantine rationale as RunSlashCommandRPC, which
	// swaps stdin for /dev/null on those surfaces. There is no human on
	// the other side of stdin to type ahead there anyway.
	if !a.cli.unattended {
		a.startStdinReader(ctx)
		defer a.stopStdinReader()
	}

	// Structured event sink for protocol frontends (ACP). Resolved per call
	// and restored on exit because this loop is re-entrant (see cancelSignal
	// note above) — an inner turn must not clear the outer turn's sink.
	prevEvents := a.events
	a.events = a.cli.agentEventSink
	defer func() { a.events = prevEvents }()

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

	// Helper local: card de RESPOSTA final do assistente com efeito de
	// máquina de escrever — restaura a sensação de "vivo" que se perdeu
	// quando a UI passou a pintar tudo num bloco lipgloss estático.
	renderAssistantMDCard := func(icon, title, md, color string) {
		md = strings.TrimSpace(md)
		if md == "" {
			return
		}
		rendered := a.cli.renderMarkdown(md)
		renderer.RenderAssistantResponseTimelineEvent(icon, title, rendered, color)
	}

	// Mid-loop skill re-activation (skill_rescan.go). pendingSkill holds a
	// skill block matched against the ASSISTANT's output; it is flushed into
	// history only at the next turn boundary so the injected user-role message
	// can never land between an assistant tool_use and its tool_result (which
	// would corrupt native tool protocols). User follow-ups inject in place —
	// their slot is already a safe turn boundary. The names ride along so the
	// flushed message carries Meta.SkillNames for skill aging.
	var pendingSkill struct {
		content string
		names   []string
	}
	notifySkillActivation := func(names []string) {
		if len(names) == 0 || a.cli.unattended {
			return
		}
		fmt.Printf("\r\033[K  %s %s\n",
			renderer.Colorize("✨", agent.ColorCyan),
			renderer.Colorize(
				i18n.T("agent.skill.rescan_activated", strings.Join(names, ", ")),
				agent.ColorCyan))
	}

	// Context recovery state for the session
	contextRecovery := agent.NewContextRecovery(agent.DefaultContextRecoveryConfig(), a.logger)
	currentMaxTokens := 0           // 0 = use provider default
	providerMaxTokensCap := 128_000 // conservative default; providers may support more
	lastSessionMaxTokens := 0       // last /max-tokens override adopted into currentMaxTokens

	// Stagnation detector — breaks out of the ReAct loop when the model
	// re-emits the SAME batch of tool_calls for N consecutive turns, which
	// is the "reflection loop" failure mode that makes trivial queries
	// burn tens of thousands of tokens. Gated by CHATCLI_AGENT_EARLY_EXIT.
	var stagnation *stagnationTracker
	if earlyExitEnabled() {
		stagnation = newStagnationTracker()
	}

	// Per-tool failure guard: complements the batch-level stagnation
	// tracker by catching a single tool that keeps failing (identical or
	// drifting args) and feeding the model targeted guidance to break the
	// cycle. Advisory only — it never alters control flow.
	// CHATCLI_AGENT_TOOLGUARD=false disables it.
	var toolGuard *toolguard.Guard
	if toolGuardEnabled() {
		toolGuard = toolguard.New(toolguard.Config{})
	}

	// --- LOOP PRINCIPAL DO AGENTE (ReAct) ---
	for turn := 0; turn < maxTurns; turn++ {
		// Verificar cancelamento pelo usuário (Ctrl+C)
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Budget hard stop: end the run before the next provider call when
		// the session budget is exhausted and CHATCLI_BUDGET_HARD_STOP is on.
		if err := a.cli.budgetBlockedErr(); err != nil {
			fmt.Println(colorize("  "+err.Error(), ColorRed))
			return err
		}

		// Honor the session /max-tokens override exactly like chat mode does.
		// Re-read every turn (raising OR lowering reflects here), while
		// preserving any truncation-driven escalation applied meanwhile.
		currentMaxTokens, lastSessionMaxTokens = adoptSessionMaxTokens(
			currentMaxTokens, lastSessionMaxTokens, a.cli.UserMaxTokens)

		// Surface background memory/skill writes (auto-extraction ticker,
		// queued notices) the moment they land instead of holding them until
		// the REPL regains control after the mode exits. Safe to drain here:
		// the go-prompt redraw loop is suspended for the whole Run(), so this
		// loop is the sole stdout writer — the race that made drain REPL-only
		// (see memory_notice.go) does not apply inside the agent loop.
		if !a.cli.unattended {
			a.cli.drainMemoryNotices()
		}

		// Drain the orchestrator's squad mailbox: workers (and the user via
		// /mail send) address it as "orchestrator". Injected at the turn
		// boundary as user context, same pattern as type-ahead below.
		if inbox := mail.Default().Drain("orchestrator"); len(inbox) > 0 {
			a.cli.history = append(a.cli.history, models.Message{
				Role: "user", Content: mail.FormatInbox(inbox),
			})
		}

		// Side commands (/agents, /board, /mail, /jobs) that could not run
		// the moment they were typed (the terminal was owned by a security
		// prompt or no live display was active) run now, before the
		// type-ahead drain — a /mail send applied here still reaches its
		// recipient's inbox drain this same turn.
		a.applySideCommands(ctx)

		// Interactive consumers (security prompts, menus) restore cooked
		// mode for themselves; re-arm cbreak each turn so the live
		// type-ahead preview self-heals.
		a.reapplyStdinCbreak()

		// Mechanical board reconciliation: cards in doing whose linked runs
		// all finished get a [BOARD SYNC] block so the orchestrator moves
		// them THIS turn (prompt discipline alone reliably decays over a
		// long ReAct loop).
		a.injectBoardSyncNotice()

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
			userMsg = a.expandFollowUpCommand(ctx, userMsg)
			a.cli.history = append(a.cli.history, models.Message{
				Role:    "user",
				Content: userMsg,
			})
			// A type-ahead follow-up is a fresh trigger surface: activate its
			// skills NOW so they shape the very next turn, not one turn late.
			if block, names := a.rescanSkillsMidLoop(userMsg, turn); block != "" {
				a.cli.history = append(a.cli.history, models.Message{
					Role:    "user",
					Content: block,
					Meta:    &models.MessageMeta{SkillNames: models.JoinSkillNames(names)},
				})
				notifySkillActivation(names)
			}
			// Same trigger surface for proactive recall: a follow-up asking
			// about past work re-ranks memory/sessions against ITS words.
			if rb := a.followUpRecallBlocks(ctx, userMsg); rb != "" {
				a.cli.history = append(a.cli.history, models.TurnContextMessage(turnContextHeader+rb))
			}
		}

		// Flush images the @view tool staged during the previous turn. Same
		// turn-boundary contract as the skill blocks below: the attachment
		// can never split a tool_use from its tool_result.
		if imgs, names := a.drainViewedImages(); len(imgs) > 0 {
			a.cli.history = append(a.cli.history, models.Message{
				Role:    "user",
				Content: "[Viewed image(s) attached: " + strings.Join(names, ", ") + "] Analyze what is visible.",
				Images:  imgs,
			})
		}

		// Flush the skill block queued by the previous turn's assistant-output
		// re-scan. This is a turn boundary: the previous turn's tool results
		// are already in history, so the injection cannot split a tool_use
		// from its tool_result.
		if pendingSkill.content != "" {
			a.cli.history = append(a.cli.history, models.Message{
				Role:    "user",
				Content: pendingSkill.content,
				Meta:    &models.MessageMeta{SkillNames: models.JoinSkillNames(pendingSkill.names)},
			})
			pendingSkill.content, pendingSkill.names = "", nil
		}

		// Same turn boundary: surface dynamic MCP tool-list changes
		// (notifications/tools/list_changed -> registry refresh). Without
		// this the model never learns about tools that appeared after a
		// bootstrap call — the system-prompt index was built at start.
		if a.cli.mcpManager != nil {
			if changes := a.cli.mcpManager.DrainToolListChanges(); len(changes) > 0 {
				a.cli.history = append(a.cli.history, models.Message{
					Role:    "user",
					Content: buildMCPToolChangeNotice(changes),
				})
				if !a.cli.unattended {
					fmt.Printf("\r\033[K  %s %s\n",
						renderer.Colorize("🔌", agent.ColorCyan),
						renderer.Colorize(
							i18n.T("agent.mcp.tools_refreshed", summarizeMCPToolChanges(changes)),
							agent.ColorCyan))
				}
			}
		}

		// Microcompact (Level 0): cheap, progressive compaction of OLD tool
		// results in history. Pure Go, no LLM call. Often keeps us below
		// budget so the expensive summarization path is never triggered.
		// Journal the previous turn's messages (tool results included) BEFORE
		// this turn's rewrites can stub them — durability against a hard
		// kill mid-run.
		a.cli.syncTranscript()
		mcCfg := agent.DefaultMicrocompactConfig()
		// Route dropped bytes through CCR so every microcompacted tool
		// result stays recoverable via @recall instead of being lost.
		mcCfg.CCR = a.cli.compressionLayer
		if h, report := agent.ApplyMicrocompact(a.cli.history, turn, mcCfg, a.logger); report != nil && (report.Truncated > 0 || report.Summarized > 0) {
			a.cli.history = h
			a.cli.costTracker.NoteExpectedCacheRebuild()
			fmt.Printf("\r\033[K  %s %s\n",
				renderer.Colorize("🗜", agent.ColorGray),
				renderer.Colorize(
					i18n.T("agent.microcompact.applied",
						report.Truncated, report.Summarized, FormatPayloadSize(int(report.CharsSaved))),
					agent.ColorGray))
		}

		// Skill aging (same turn boundary as microcompact, so both passes
		// share a single prefix-cache invalidation event): mid-loop skill
		// blocks the model has already absorbed collapse to CCR-recoverable
		// stubs, and the collapsed skills leave the dedup set so they can
		// re-trigger after the cooldown.
		saCfg := agent.DefaultSkillAgingConfig()
		saCfg.CCR = a.cli.compressionLayer
		// Repeated reads of the same file: keep the newest, stub the rest
		// (recoverable via @recall). Same turn boundary as microcompact so
		// the two rewrites share one prefix-cache invalidation.
		if h, report := agent.DedupRepeatedReads(a.cli.history, mcCfg.CCR, a.logger); report != nil && report.Superseded > 0 {
			a.cli.history = h
			a.cli.costTracker.NoteExpectedCacheRebuild()
			fmt.Printf("\r\033[K  %s %s\n",
				renderer.Colorize("│", agent.ColorGray),
				renderer.Colorize(i18n.T("agent.dedup_reads.applied", report.Superseded, FormatPayloadSize(int(report.CharsSaved))), agent.ColorGray))
		}
		if h, report := agent.ApplySkillAging(a.cli.history, saCfg, a.logger); report != nil && report.Collapsed > 0 {
			a.cli.history = h
			a.cli.costTracker.NoteExpectedCacheRebuild()
			a.releaseCollapsedSkills(report.CollapsedSkills, turn)
			fmt.Printf("\r\033[K  %s %s\n",
				renderer.Colorize("🗜", agent.ColorGray),
				renderer.Colorize(
					i18n.T("agent.skills.aged",
						report.Collapsed, FormatPayloadSize(int(report.CharsSaved))),
					agent.ColorGray))
		}

		// Compact history if over budget (before building turn history)
		cfg := a.cli.compactConfig(a.cli.Provider, a.cli.Model)
		cfg.ReservedChars = a.toolDefsChars
		// Tighter mode default (tool outputs are large) — unless the user
		// (/autocompact) or the catalog declared a threshold, which wins.
		if a.cli.autoCompact.get() <= 0 && catalog.GetCompactRatio(a.cli.Provider, a.cli.Model) <= 0 {
			cfg.BudgetRatio = 0.60
		}
		cfg.MinKeepRecent = 8 // ~4 tool call cycles

		// Pre-flight: measure the current history and react BEFORE the
		// request goes out. Two paths:
		//   (a) User has CHATCLI_MAX_PAYLOAD set and we're above 85%
		//       of it → force aggressive compaction this turn.
		//   (b) No explicit cap but history is already > 2.5 MB → emit a
		//       one-shot hint about the env var. Corporate proxies usually
		//       cap at 5 MB and the error they return is obscure (403/EOF),
		//       so flagging this early saves the user a painful surprise.
		totalHistoryChars := totalChars(a.cli.history)
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
			a.cli.flushMemoryBeforeCompaction(ctx)
			// Emit live status during compaction so the terminal is never
			// silent. Without this, Level 2 (LLM summarization) can block
			// for 30-90s with zero feedback — users assume a freeze.
			a.cli.historyCompactor.SetStatusCallback(func(stage CompactStage, msg string) {
				fmt.Printf("\r\033[K  %s %s\n",
					renderer.Colorize("│", agent.ColorCyan),
					renderer.Colorize(msg, agent.ColorGray))
			})
			a.cli.beforeCompaction(ctx, compactTriggerAuto)
			compacted, compactErr := a.cli.historyCompactor.Compact(ctx, a.cli.history, a.cli.Client, cfg)
			a.cli.historyCompactor.SetStatusCallback(nil)
			switch {
			case compactErr == nil && !historiesEqual(compacted, a.cli.history):
				a.cli.history = compacted
				a.cli.noteCompactionApplied(ctx, compactTriggerAuto)
			case errors.Is(compactErr, context.Canceled):
				a.cli.compactionSkipped(ctx, compactTriggerAuto)
				return compactErr
			default:
				a.cli.compactionSkipped(ctx, compactTriggerAuto)
			}
		}

		a.logger.Debug("Iniciando turno do agente", zap.Int("turn", turn+1), zap.Int("max_turns", maxTurns))

		// Reset per-turn counters
		turnAgents := 0
		turnToolCalls := 0

		// Resolve per-turn client + effort hint from any active "@model use"
		// route override or skill hints. Model swap is transparent; effort
		// flows via ctx so the provider's SendPrompt can enable extended
		// thinking / reasoning. Resolved BEFORE the turn timer starts so the
		// spinner names the model that actually serves this turn — labeling
		// it with the session model while an override routes the call
		// elsewhere makes the UI contradict the logs.
		turnClient, turnCtx := a.clientAndCtxForTurn(ctx)

		// Inicia o timer do turno (substitui a animação de "Pensando...").
		// Item 8: indicator reflete TANTO as linhas já drenadas para a
		// messageQueue QUANTO as linhas em trânsito no channel
		// stdinLines (Enter foi pressionado mas o turn atual ainda não
		// fechou para drenar). Sem essa soma, o usuário pressiona Enter
		// e nada muda visualmente no spinner — só vê (1 na fila) na
		// próxima iteração do turn.
		modelName := turnClient.GetModelName()
		// Tracks whether the previous tick painted a type-ahead line under
		// the spinner. Written only by the ticker goroutine (under the
		// timer mutex); read after Stop() for the final cleanup.
		spinnerHadPreview := false
		a.turnTimer.Start(ctx, func(d time.Duration) {
			var frame string
			frame, spinnerHadPreview = a.buildTurnSpinnerFrame(d, modelName, spinnerHadPreview)
			fmt.Print(frame)
		})

		// Validate/repair tool result pairing on the PERSISTENT history —
		// not just the outgoing copy — so the repaired shape survives into
		// later turns and into park snapshots. Repairing only the per-turn
		// copy let dangling tool_calls live forever in a.cli.history and
		// resurface on every park/resume cycle.
		repairedHistory, pairingReport := agent.EnsureToolResultPairing(a.cli.history, a.logger)
		if pairingReport.HasRepairs() {
			a.cli.history = repairedHistory
			a.logger.Info("Tool result pairing repaired before API call",
				zap.Int("synthetic_results", pairingReport.SyntheticResultsInjected),
				zap.Int("orphans_removed", pairingReport.OrphanResultsRemoved),
				zap.Int("results_relocated", pairingReport.ResultsRelocated))
		}

		// Build the outgoing turn history and enforce budget before sending to API
		turnHistory := buildTurnHistoryWithAnchor()
		turnHistory, _ = agent.EnforceToolResultBudget(turnHistory, a.logger)

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
				// The refresh rebuilt the session client; re-resolve the
				// turn client so the retry runs on a fresh handle for the
				// provider that actually served this turn (route override
				// included) instead of the stale pre-refresh wrapper.
				turnClient, turnCtx = a.clientAndCtxForTurn(ctx)
				if tac, ok := llmclient.AsToolAware(turnClient); ok && tac.SupportsNativeTools() {
					toolAwareClient = tac
				}
				llmResp, err = toolAwareClient.SendPromptWithTools(turnCtx, "", turnHistory, nativeToolDefs, currentMaxTokens)
			}
			if err == nil && llmResp != nil {
				aiResponse = llmResp.Content
				nativeToolCalls = llmResp.ToolCalls
			}
		} else {
			aiResponse, err = turnClient.SendPrompt(turnCtx, "", turnHistory, currentMaxTokens)
			if a.cli.refreshClientOnAuthError(err) {
				turnClient, turnCtx = a.clientAndCtxForTurn(ctx)
				aiResponse, err = turnClient.SendPrompt(turnCtx, "", turnHistory, currentMaxTokens)
			}
		}

		// Track cost for agent mode turn — prefer real API usage.
		// Attribute to whichever provider+model the resolver actually
		// served this turn (see clientAndCtxForTurn). turnUsage is hoisted
		// so the per-turn telemetry line (showTurnStats) can reuse it.
		var turnUsage *models.UsageInfo
		if a.cli.costTracker != nil && err == nil {
			inputChars := 0
			for _, m := range turnHistory {
				inputChars += len(m.Content)
			}
			turnUsage = llmclient.GetUsageOrEstimate(turnClient, inputChars, len(aiResponse))
			effProvider, effModel := a.effectiveRoute()
			a.cli.costTracker.RecordRealUsage(effProvider, effModel, turnUsage)
			// The provider context engine cleared tool results server-side:
			// mirror that locally and do not calibrate on this turn (the
			// chars sent no longer match the tokens counted).
			edited := llmResp != nil && llmResp.ContextEdits != nil && llmResp.ContextEdits.ClearedToolUses > 0
			if edited {
				a.cli.mirrorContextEdits(llmResp.ContextEdits)
			}
			// Learn the real chars-per-token ratio for this provider/model
			// from what was actually sent and what the API counted.
			if turnUsage != nil && turnUsage.IsReal && !edited {
				a.cli.calibrator().Observe(effProvider, effModel, promptCharsOf(turnHistory), contextTokens(effProvider, effModel, turnUsage))
			}
			a.cli.maybeAnnounceBudget()
			a.cli.maybeAnnounceCacheMisses()
		}

		// Para o timer e obtém a duração
		turnDuration := a.turnTimer.Stop()
		fmt.Print(metrics.ClearLine()) // Limpa a linha do timer
		// If the user was mid-typing when the turn ended, a type-ahead line
		// is still painted below the spinner — wipe it so the response
		// doesn't interleave with a stale preview. (Safe read: Stop()
		// already synchronized with the last tick.)
		fmt.Print(spinnerPreviewWipe(spinnerHadPreview))
		fmt.Println()

		// Helper para exibir métricas ao final do turno (após execução)
		showTurnStats := func() {
			// Gateway/unattended: per-turn stats ("Turn 1/100 8s") are operator
			// telemetry, not chat content — the action feed already conveys
			// progress. Skip them so the feed stays concise.
			if a.cli.unattended {
				return
			}
			// Live telemetry (tokens · ctx% · session cost · savings), reusing
			// the exact data sources behind chat mode's envelope footer so the
			// user stays aware of spend/context inside agent & coder too.
			var telem string
			if turnUsage != nil && a.cli.costTracker != nil {
				telem = strings.Join(a.cli.telemetryParts(turnUsage, a.cli.costTracker.TotalCost(), true), " · ")
			}
			fmt.Println(metrics.FormatTurnInfo(turn+1, maxTurns, turnDuration, &metrics.TurnStats{
				TurnAgents:       turnAgents,
				TurnToolCalls:    turnToolCalls,
				SessionAgents:    a.agentsLaunched,
				SessionToolCalls: a.toolCallsExecd,
				Telemetry:        telem,
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
				// just retry with the same size and fail again. Providers
				// annotate the error with the exact request size the
				// middlebox rejected (client.WithRequestSize) — learn the cap
				// from it; fall back to a 4 MB guess only when unknown.
				rejectedBytes, hasRejectedSize := llmclient.RequestSizeFromError(err)
				if isPayloadTooLarge {
					currentCap := ParsePayloadSize(os.Getenv("CHATCLI_MAX_PAYLOAD"))
					switch {
					case hasRejectedSize:
						learnedCap := rejectedBytes * 3 / 4
						if currentCap == 0 || learnedCap < currentCap {
							_ = os.Setenv("CHATCLI_MAX_PAYLOAD", fmt.Sprintf("%dB", learnedCap))
							fmt.Printf("  %s %s\n",
								renderer.Colorize("ℹ", agent.ColorGray),
								renderer.Colorize(
									i18n.T("agent.recovery.learned_cap",
										FormatPayloadSize(rejectedBytes),
										FormatPayloadSize(learnedCap)),
									agent.ColorGray))
						}
					case currentCap == 0:
						// Educated guess: assume a 4 MB proxy cap as a sane
						// default when the rejected size is unknown and the
						// user hasn't configured one.
						_ = os.Setenv("CHATCLI_MAX_PAYLOAD", "4MB")
						fmt.Printf("  %s %s\n",
							renderer.Colorize("ℹ", agent.ColorGray),
							renderer.Colorize(
								i18n.T("agent.recovery.assumed_cap"),
								agent.ColorGray))
					}

					// Floor diagnosis: system messages (agent charter, skills,
					// personas, MCP tool docs) are never compacted. When they
					// alone reach the size the gateway just rejected, no
					// amount of history compaction can produce an acceptable
					// request — fail fast with an actionable message instead
					// of burning recovery attempts on identical payloads.
					if hasRejectedSize {
						systemChars := 0
						for _, m := range a.cli.history {
							if m.Role == "system" {
								systemChars += len(m.Content)
							}
						}
						if systemChars >= rejectedBytes {
							diag := i18n.T("agent.recovery.floor_exceeds_cap",
								FormatPayloadSize(systemChars),
								FormatPayloadSize(rejectedBytes))
							fmt.Printf("\r\033[K  %s %s\n",
								renderer.Colorize("✖", agent.ColorRed),
								renderer.Colorize(diag, agent.ColorRed))
							return fmt.Errorf("%s: %w", diag, err)
						}
						if systemChars >= rejectedBytes*3/4 {
							fmt.Printf("  %s %s\n",
								renderer.Colorize("⚠", agent.ColorYellow),
								renderer.Colorize(
									i18n.T("agent.recovery.floor_warn",
										FormatPayloadSize(systemChars)),
									agent.ColorYellow))
						}
					}
				}

				// Recovery drops whole messages without a summary: flush what
				// the memory worker has not seen yet, tell the hooks, archive
				// the dropped messages to CCR and account for the rebuild —
				// the same guarantees as a planned compaction.
				a.cli.flushMemoryBeforeCompaction(ctx)
				a.cli.beforeCompaction(ctx, compactTriggerRecovery)
				recoveredHistory, recovered := contextRecovery.RecoverContextOverflow(a.cli.history)
				if !recovered {
					a.cli.compactionSkipped(ctx, compactTriggerRecovery)
				}
				if recovered {
					if note := archiveDroppedMessages(a.cli.compressionLayer, a.cli.history, recoveredHistory); note != "" {
						recoveredHistory = append(recoveredHistory, models.Message{Role: "user", Content: note})
					}
					a.cli.history = recoveredHistory
					a.cli.costTracker.NoteExpectedCacheRebuild()
					a.cli.firePostCompact(ctx, compactTriggerRecovery)

					// Post-recovery hint for payload-related failures: without
					// this, the model tends to re-read the same huge file that
					// triggered the limit, looping through recovery until
					// MaxRecoveryAttempts is exhausted. A single user-role
					// instruction steers it toward surgical, line-ranged reads
					// that fit under the cap. Injected at most once: appending
					// it on every recovery attempt GROWS the payload while the
					// middlebox demands it shrink.
					if isPayloadTooLarge && !historyContainsPayloadHint(a.cli.history) {
						a.cli.history = append(a.cli.history, models.Message{
							Role: "user",
							Content: payloadRecoveryHintMarker + " A proxy/gateway rejected the previous request due to body size. History was compacted to recover. " +
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
		} else if src, ok := llmclient.AsStopReasonAware(turnClient); ok {
			// turnClient served this call; under a route override the
			// session client's last stop reason belongs to another turn.
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

		// Mid-loop skill activation: the model's own <reasoning> text and the
		// file paths inside its tool_call args are trigger surfaces too. A
		// skill whose keyword only surfaces in the agent's plan ("I'll write a
		// Helm chart for this") — or whose path glob matches a file the agent
		// just started touching — is injected at the next turn boundary, in
		// time to improve the remaining work. Deduped per Run, so a skill the
		// user's query already activated never re-fires here. Native tool
		// calls carry their args OUTSIDE the response text, so they are
		// appended to the scanned surface explicitly — otherwise path-glob
		// skills would only ever fire on the XML fallback.
		if block, names := a.rescanSkillsMidLoop(rescanSurface(aiResponse, nativeToolCalls), turn); block != "" {
			pendingSkill.content, pendingSkill.names = block, names
			notifySkillActivation(names)
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
				// Close the just-persisted native batch first: stagnation
				// fires BEFORE execution, so without this the run ends with
				// assistant(tool_calls) + user — the dangling shape strict
				// providers reject on the next request.
				a.closeNativeBatch(nativeToolCalls, nil, toolResultNotExecutedStagnation)
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
			a.emitThought(reasoning)
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
			a.emitThought(explanation)
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
			a.emitPlan()
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
			// Capture the clean prose for non-interactive callers (the gateway
			// delivers cli.lastAgentReply as the final chat answer). Last-wins:
			// each turn's prose overwrites the previous, so the value left
			// standing after the loop is the model's final answer. Only the
			// ReAct/coder path sets this here; the legacy agent one-shot sets it
			// in RunOnce. Harmless for interactive runs, which never read it.
			a.cli.lastAgentReply = strings.TrimSpace(remaining)
			a.cli.reinforceRecalledFacts(a.cli.lastAgentReply)
			// Structured sinks receive the prose regardless of the terminal
			// rendering decision below — under ACP the run is unattended, so
			// without this the final answer never reached the client.
			a.emitMessage(remaining)
			switch {
			case a.cli.unattended:
				// Gateway/unattended: the captured lastAgentReply is delivered
				// once as the clean final chat answer, so don't also render the
				// prose into the action feed — doing both made the reply arrive
				// twice (the raw 💬 RESPOSTA card and then the polished send).
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
				renderAssistantMDCard("💬", "RESPOSTA", remaining, agent.ColorGray)
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
				if looksLikeLooseCode(aiResponse) {
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
			// Malformed <agent_call> tags are dropped by the parser without a
			// trace; left silent, the model never learns its dispatch failed
			// and drifts out of the squad flow into direct tool calls. Feed
			// the error back so the NEXT turn re-emits a corrected dispatch.
			if attempts := workers.CountAgentCallTags(aiResponse); attempts > len(agentCalls) {
				dropped := attempts - len(agentCalls)
				fmt.Println(colorize("  ⚠ "+i18n.T("agent.squad.malformed_call", dropped), ColorYellow))
				a.cli.history = append(a.cli.history, models.Message{
					Role:    "user",
					Content: workers.MalformedAgentCallFeedback(dropped, len(agentCalls)),
				})
			}
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
					// Enrich each slot with live progress from the run
					// registry: current ReAct turn, action in flight and any
					// subagents the worker spawned (rendered as sub-lines).
					reg := runs.Default()
					for _, ac := range agentCalls {
						info, ok := reg.ByCallID(ac.ID)
						if !ok {
							continue
						}
						var subLines []string
						for _, child := range reg.Children(info.ID) {
							subLines = append(subLines, formatRunChildLine(child))
						}
						progressState.SetLive(ac.ID, info.Turn, info.MaxTurns, info.Action, subLines)
					}
					output := metrics.FormatDispatchProgress(progressState, modelName)
					if previewLine := formatTypeaheadPreviewLine(a.typeaheadPreviewSnapshot()); previewLine != "" {
						output += previewLine + "\n"
					}
					fmt.Print(output)
					// Count rendered lines directly — sub-lines make the
					// panel height dynamic between ticks.
					prevLines = strings.Count(output, "\n")
				})

				// Give the policy adapter access to the spinner and stdin
				// channel so it can pause/resume around interactive
				// security prompts and read input without orphaning goroutines.
				if a.policyAdapter != nil {
					a.policyAdapter.setSpinner(a.turnTimer)
					a.policyAdapter.setStdinCh(a.stdinLines)
					a.policyAdapter.setRestoreInput(a.reapplyStdinCbreak)
				}
				// Workers must follow the model that serves THIS turn. The
				// dispatcher's pair was captured at Run() start, before the
				// AI could take a @model route override mid-task; without
				// this refresh every delegated worker kept talking to the
				// model the orchestrator had just switched away from.
				a.agentDispatcher.UpdateProviderModel(a.effectiveRoute())
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

				// Inject results as feedback for the orchestrator. The
				// AgentFeedback meta lets microcompact age this block like
				// tool output — as a plain user message it was invisible to
				// every reduction mechanism short of full compaction.
				feedback := workers.FormatResults(agentResults)
				a.cli.history = append(a.cli.history, models.Message{
					Role:    "user",
					Content: feedback,
					Meta:    &models.MessageMeta{AgentFeedback: true},
				})

				// If there are also tool_calls in the same response, skip them —
				// the orchestrator should use agent_calls OR tool_calls, not both
				// in the same turn. Native calls must still be CLOSED: leaving
				// them unanswered ships the dangling shape strict providers 400
				// on whenever the loop terminates right after this turn.
				if len(toolCalls) > 0 {
					a.logger.Info("Skipping tool_calls because agent_calls were dispatched in this turn")
					a.closeNativeBatch(nativeToolCalls, nil, toolResultNotExecutedAgentCalls)
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

			// guardGuidance collects any tool-loop hints produced this turn;
			// appended to history once, after the tool results.
			var guardGuidance []string

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
						// Slash-command allowed-tools overlay: a tool outside
						// the command's declared list escalates to "ask" even
						// when policy would allow — the command author scoped
						// the run, the human (or policy automode) arbitrates
						// the exception. Never a silent widen, never a silent
						// deny.
						if action == coder.ActionAllow && !a.commandScopeAllows(tc.Name) {
							if a.logger != nil {
								a.logger.Info("tool outside slash-command allowed-tools scope, escalating to ask",
									zap.String("tool", tc.Name))
							}
							action = coder.ActionAsk
						}
						// Session automode (/policy mode auto): "ask" verdicts
						// auto-approve. Deny rules were already resolved above
						// ask by the policy check, and safety-immune operations
						// never take this shortcut — they keep prompting.
						if action == coder.ActionAsk && a.askAutoApproved(tc.Name, tc.Args) {
							if a.logger != nil {
								a.logger.Info("coder policy: 'ask' auto-approved (session automode)",
									zap.String("tool", tc.Name))
							}
							action = coder.ActionAllow
						}
						if action == coder.ActionDeny {
							msg := "AÇÃO BLOQUEADA (Regra de Segurança)"
							renderError(msg)
							a.emitBlockedTool(tc.Name, tc.Args, msg)
							a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: "ERRO: " + msg})
							batchHasError = true
							break
						}
						if action == coder.ActionAsk && a.cli.unattended {
							// Unattended: PromptSecurityCheck would block on a
							// dead stdin forever (os.Stdin may even carry the
							// JSON-RPC channel). When the connected client offers
							// a permission dialog (ACP request_permission, MCP
							// elicitation) the human decides there; without one,
							// the historical contract holds — the operator opted
							// into autonomy, so "ask" auto-approves. An explicit
							// ActionDeny above still blocks either way.
							if a.unattendedAskBlocked(tc.Name, tc.Args, pm, renderError) {
								batchHasError = true
								break
							}
						} else if action == coder.ActionAsk {
							decision := coder.PromptSecurityCheckGuarded(ctx, tc.Name, tc.Args, a.stdinLines)
							// The prompt forced cooked mode (stty sane); re-arm
							// cbreak so live type-ahead survives the rest of
							// the turn instead of degrading until the boundary.
							a.reapplyStdinCbreak()
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

				// Structured event: tool call entering execution. The title
				// mirrors the spinner label logic (DescribeCall when the
				// plugin offers one) so IDE clients see "Reading: main.go"
				// instead of a raw args string. eventTC is completed by the
				// matching emitToolEnd at the structured-capture point below.
				eventTitle := defaultSpinnerLabel(toolName, toolArgs)
				if a.events != nil {
					if p, ok := a.cli.pluginManager.GetPlugin(toolName); ok && p != nil {
						if d := plugins.DescribeCall(p, toolArgs); d != "" {
							eventTitle = d
						}
					}
				}
				eventTC := a.emitToolStart(toolName, eventTitle, normalizedArgsStr, toolArgs)

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
						// When needsSchema is set the schema was already returned to
						// the model above; skip execution this turn.
						if !needsSchema {
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
							// Close the batch before bailing: the assistant
							// tool_calls message is already persisted, and an
							// unanswered batch left behind is exactly the
							// dangling shape strict providers 400 on — in
							// chat mode too, which has no repair pass.
							a.closeNativeBatch(nativeToolCalls, turnToolResults, toolResultNotExecutedCanceled)
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
						// When a structured sink provides a PermissionRequester (ACP), the
						// human decides through the client's native dialog instead — an
						// explicit approval there runs the command; anything else keeps
						// the block exactly as before.
						if a.isCoderMode && !batchHasError {
							if dangerous, shellCmd := a.isCoderExecDangerous(toolArgs); dangerous {
								allowed, asked := a.requestActionPermission(eventTC,
									i18n.T("agent.permission.dangerous_exec", shellCmd))
								if !asked || !allowed {
									msg := fmt.Sprintf(
										"BLOCKED: Dangerous command detected in @coder exec: %q. "+
											"This command is forbidden regardless of policy rules. "+
											"DO NOT retry this command.", shellCmd)
									if asked {
										msg = fmt.Sprintf(
											"BLOCKED: Dangerous command %q was DENIED by the user. "+
												"DO NOT retry this command.", shellCmd)
									}
									renderError(msg)
									a.cli.history = append(a.cli.history, models.Message{
										Role: "user", Content: "SECURITY BLOCK: " + msg,
									})
									batchHasError = true
									execErr = fmt.Errorf("dangerous command blocked: %s", shellCmd)
								}
							}
						}
						// --- END DANGEROUS EXEC GUARD ---

						// Se não houve erro de validação, EXECUTA
						if !batchHasError {
							// Fire PreToolUse hook — may block the action
							if a.cli.hookManager != nil {
								wd, _ := os.Getwd()
								hookResult := a.cli.hookManager.Fire(ctx, hooks.HookEvent{
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
								// First real output ends the spinner so streamed
								// lines don't clash with its carriage-return repaint.
								a.cli.animation.StopThinkingAnimation()
								if isCompact {
									return
								}
								// Serialize with worker security prompts: a tool
								// that dispatches workers (@taskgraph) streams from
								// background goroutines, and an ungated print lands
								// on top of the prompt box — corrupting both the
								// render and the answer the user is typing. When
								// the turn spinner is live (taskgraph runs), the
								// print also pauses/resumes it so event lines and
								// the spinner/preview repaint never interleave.
								if a.policyAdapter != nil {
									a.policyAdapter.withPromptGate(func() {
										if t := a.turnTimer; t != nil && t.IsRunning() {
											t.Pause()
											renderer.StreamOutput(line)
											t.Resume()
											return
										}
										renderer.StreamOutput(line)
									})
									return
								}
								renderer.StreamOutput(line)
							}

							// Marca tarefa como em andamento ANTES de executar
							agent.MarkTaskInProgress(a.taskTracker)

							// @taskgraph dispatches workers whose security
							// prompts run on the shared policy adapter — re-arm
							// its spinner/stdin/cbreak hooks exactly like the
							// <agent_call> dispatch path does, otherwise the
							// prompts race the central stdin reader for keys and
							// typed answers get mangled into denials. The turn
							// spinner also runs for the whole graph execution so
							// the user's typing renders as the live ❯ …▌ preview
							// (streamed event lines pause/resume it — see
							// streamCallback) instead of a dead keyboard.
							taskGraphTimerStarted := false
							tgHadPreview := false
							if strings.EqualFold(strings.TrimSpace(toolName), "@taskgraph") && a.policyAdapter != nil {
								a.policyAdapter.setSpinner(a.turnTimer)
								a.policyAdapter.setStdinCh(a.stdinLines)
								a.policyAdapter.setRestoreInput(a.reapplyStdinCbreak)
								a.turnTimer.SetOnPause(func() {
									fmt.Print(metrics.ClearLine())
									fmt.Print(spinnerPreviewWipe(tgHadPreview))
									tgHadPreview = false
								})
								a.turnTimer.Start(ctx, func(d time.Duration) {
									var frame string
									frame, tgHadPreview = a.buildTurnSpinnerFrame(d, taskGraphSpinnerLabel, tgHadPreview)
									fmt.Print(frame)
								})
								taskGraphTimerStarted = true
							}

							// Delegate interception: if this is an @coder call with
							// cmd=delegate, it's a subagent spawn — NOT a plugin
							// invocation. Route to workers.RunDelegate with our
							// LLM client so the subagent has its own isolated
							// context.
							if strings.EqualFold(strings.TrimSpace(toolName), "@ask") {
								// @ask interception: the loop owns the TTY and the
								// stdin reader, so it (not the pure plugin) renders
								// the interactive overlay and feeds the answers back
								// synchronously as this turn's tool result.
								toolOutput, execErr = a.handleAgentAsk(ctx, normalizedArgsStr)
							} else if strings.EqualFold(strings.TrimSpace(toolName), "@coder") && isDelegateInvocation(normalizedArgsStr) {
								nativeArgs, rawInner := extractDelegateArgs(normalizedArgsStr)
								// turnClient, not a.cli.Client: the subagent must
								// run on the model serving this turn (route
								// override / skill hint included), not on the
								// session client the orchestrator switched away from.
								toolOutput, execErr = workers.RunDelegate(
									ctx,
									nativeArgs,
									rawInner,
									turnClient,
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
									// Animated spinner during execution so blocking
									// tools (network I/O: @moa, @image, @webfetch…)
									// never feel frozen. Streaming tools stop it on
									// their first line (see streamCallback); it's
									// always stopped after the call returns.
									if !isCompact {
										a.cli.animation.ShowThinkingAnimation(spinnerMessage(boxLabel))
									}
									toolOutput, execErr = plugin.ExecuteWithStream(ctx, toolArgs, streamCallback)
									a.cli.animation.StopThinkingAnimation()
								}
							}

							if taskGraphTimerStarted {
								a.turnTimer.Stop()
								a.turnTimer.SetOnPause(nil)
								fmt.Print(metrics.ClearLine())
								fmt.Print(spinnerPreviewWipe(tgHadPreview))
							}

							if !isCompact {
								renderer.RenderStreamBoxEnd(agent.ColorPurple)
							}

							// Se o contexto foi cancelado (Ctrl+C), propaga
							// imediatamente — fechando o batch antes, para o
							// histórico durável nunca ficar com tool_calls
							// sem resposta (o formato que providers estritos
							// rejeitam com 400, inclusive via chat).
							if ctx.Err() != nil {
								a.closeNativeBatch(nativeToolCalls, turnToolResults, toolResultNotExecutedCanceled)
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
								var beforeClosure []models.Message
								if i < len(nativeToolCalls) {
									pendingID = nativeToolCalls[i].ID
									// Close every OTHER native call of this
									// batch before snapshotting: the loop
									// suspends here, so the structured
									// emission tail below never runs. Without
									// this, calls batched alongside @park
									// (e.g. web_fetch × 2 + park) stay
									// dangling in the snapshot forever and
									// every resume ships an invalid history
									// (Moonshot/OpenAI-compat 400). Only the
									// park call itself stays pending —
									// RunResumed pairs it with the real park
									// outcome.
									beforeClosure = a.cli.history
									a.cli.history = insertStructuredToolResults(a.cli.history,
										buildParkBatchClosure(nativeToolCalls, turnToolResults, i))
								}
								parkErr := a.handleAgentPark(ctx, req, pendingID, toolName)
								if parkErr != nil && !errors.Is(parkErr, errAgentParkedRequested) && beforeClosure != nil {
									// Park never armed (snapshot save / enqueue
									// failed): the closures claiming "the agent
									// parked" would be false history. Restore
									// the pre-closure slice — the next turn's
									// pairing repair closes the batch honestly.
									a.cli.history = beforeClosure
								}
								return parkErr
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
					a.cli.hookManager.FireAsync(ctx, hooks.HookEvent{
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

				// Content-aware, reversible compression of the LLM-facing tool
				// output (search/log/diff/JSON). This is the single chokepoint
				// for both the structured-native path (turnToolResults below)
				// and the legacy batch text path, so it engages in agent and
				// coder mode alike. Humans already saw the full output (above)
				// and the PostToolUse hook already received it; only the model's
				// copy is reduced, with the original recoverable via @recall.
				// Errors are left verbatim so the model can debug them in full.
				// CompressToolOutput is nil-safe and a no-op when disabled or
				// below the size threshold.
				// Secret redaction on the model's copy (CHATCLI_ENV_REDACT_MODE):
				// name-based KEY=VALUE masking plus value-shape scan, applied to
				// every tool output — file reads and plugin results included,
				// not only exec — and to error text, which quotes inputs.
				toolOutput = redactSecretsForLLM(toolOutput)
				if execErr == nil && a.cli != nil {
					toolOutput, _ = a.cli.compressionLayer.CompressToolOutput(tc.Name, toolOutput)
				}

				// Per-tool truncation (Item 6) BEFORE the structured capture:
				// turnToolResults feeds the persistent history and park
				// snapshots via the structured emission paths, so an
				// untruncated multi-hundred-KB fetch must never be captured.
				// Plugins that implement plugins.TruncationAware get their
				// own per-call cap; the rest use the global default. Errors
				// are left verbatim so the model can debug them in full.
				if execErr == nil {
					maxChars := plugins.DefaultMaxResultChars
					if p, ok := a.cli.pluginManager.GetPlugin(tc.Name); ok && p != nil {
						maxChars = plugins.EffectiveMaxResultChars(p)
					}
					toolOutput = plugins.TruncateForLLM(toolOutput, maxChars)
				}

				// Capture the structured outcome for downstream phases.
				// Duration is wall-clock for this single tool — Fase 3 will
				// also use it to size the per-batch concurrency budget.
				structured := agent.WrapLegacyOutput(toolOutput, execErr)
				structured.Duration = time.Since(toolStartTime)
				turnToolResults = append(turnToolResults, structured)

				// Structured event: terminal state for this tool call, using
				// the LLM-facing output (already compressed+truncated above) —
				// never displayForHuman, whose streamed copy the sink client
				// already renders once.
				a.emitToolEnd(eventTC, toolOutput, execErr, structured.ErrorCode, structured.Duration)

				// Feed the per-tool failure guard. Guidance (if any) is
				// injected into history after the batch results below.
				if toolGuard != nil {
					// A successful tool WITH side effects resets the
					// repeat-success (doom-loop) tracking: the world changed,
					// so re-running a read may legitimately differ now.
					// plugins.IsReadOnly fails closed (unknown = mutating).
					if execErr == nil {
						if p, ok := a.cli.pluginManager.GetPlugin(tc.Name); ok {
							if !plugins.IsReadOnly(p, []string{normalizedArgsStr}) {
								toolGuard.NoteStateChange()
							}
						} else {
							toolGuard.NoteStateChange()
						}
					}
					errMsg := ""
					if execErr != nil {
						errMsg = execErr.Error()
					}
					if d := toolGuard.Observe(toolName, normalizedArgsStr, errMsg, execErr != nil); d.Guidance != "" {
						guardGuidance = append(guardGuidance, d.Guidance)
					}
				}

				// Acumula o resultado para a LLM
				batchOutputBuilder.WriteString(fmt.Sprintf("--- Resultado da Ação %d (%s) ---\n", i+1, toolName))

				if execErr != nil || batchHasError {
					batchOutputBuilder.WriteString(fmt.Sprintf("ERRO: %v\nSaída parcial: %s\n", execErr, toolOutput))
					batchOutputBuilder.WriteString("\n[EXECUÇÃO EM LOTE INTERROMPIDA PREMATURAMENTE DEVIDO A ERRO NA AÇÃO ANTERIOR]\n")

					// Garante flag de erro se veio de execErr
					batchHasError = true
					break // Fail-Fast: Para a execução do lote
				}

				// Per-tool truncation already applied above, before the
				// structured capture — both the legacy concatenation and
				// turnToolResults see the same bounded output.
				batchOutputBuilder.WriteString(toolOutput)
				batchOutputBuilder.WriteString("\n\n")
				successCount++
				a.toolCallsExecd++
				turnToolCalls++
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

			// Provider-agnostic tool_result emission: when the assistant
			// produced native tool_calls in this turn, EVERY call gets a
			// Role="tool" message — real outcomes for the executed prefix
			// (the batch is fail-fast, so turnToolResults aligns
			// index-for-index with the first calls) and "not executed"
			// closures for the rest. This must hold even on mid-batch
			// errors: skipping the emission leaves dangling tool_calls
			// that strict OpenAI-compat providers reject with 400, and
			// providers with deterministic per-turn IDs (Kimi K3:
			// "web_fetch:0") defeat any later ID-based repair — so the
			// shape is closed at the source. Results are INSERTED right
			// after the assistant message, before any feedback appended
			// mid-batch (security blocks, format-fix prompts), preserving
			// the adjacency every native tool API validates. Each provider
			// adapter (claudeai, openai, moonshot, minimax, zai,
			// openrouter) already translates role="tool" into its native
			// shape — Anthropic tool_result with is_error, OpenAI-family
			// tool message with [ERROR:<code>] marker.
			//
			// The legacy concatenated user message remains ONLY for the
			// text-mode XML dispatch path, where no tool_use IDs exist.
			if len(nativeToolCalls) > 0 {
				a.cli.history = insertStructuredToolResults(a.cli.history,
					buildNativeBatchResults(nativeToolCalls, turnToolResults, toolResultNotExecutedBatchError))
				// Optional replanning hint as a side note, kept as a
				// system-style nudge rather than a malformed user message.
				if a.taskTracker != nil && a.taskTracker.NeedsReplanning() {
					a.cli.history = append(a.cli.history, models.Message{
						Role:    "user",
						Content: "ATENÇÃO: Múltiplas falhas detectadas. Crie um NOVO <reasoning> com uma lista replanejada de tarefas, considerando os erros anteriores.",
					})
				}
			} else if batchHasError && !strings.Contains(batchOutputBuilder.String(), "Resultado da Ação") {
				// Erro de validação sem nenhuma ação executada (via XML):
				// a msg de correção específica já está no histórico —
				// apenas damos continue para a IA tentar corrigir.
				continue
			} else {
				// Legacy path: text-mode dispatch or partially-failed batch.
				// One user message carries everything; provider adapters
				// see plain text without tool_result block semantics.
				// The label lists the REAL tool names: an internal
				// placeholder ("batch_execution") leaked into the model's
				// visible prose — it would narrate the made-up tool name
				// back to the user.
				feedbackForAI := i18n.T("agent.feedback.tool_output", toolCallNamesLabel(toolCalls), batchOutputBuilder.String())
				if a.taskTracker != nil && a.taskTracker.NeedsReplanning() {
					feedbackForAI += "\n\nATENÇÃO: Múltiplas falhas detectadas. Crie um NOVO <reasoning> com uma lista replanejada de tarefas, considerando os erros anteriores."
				}
				a.cli.history = append(a.cli.history, buildBatchFeedbackMessage(feedbackForAI, toolCalls))
			}

			// Inject tool-loop guidance (if the guard fired this turn) so the
			// model can change approach instead of retrying the same failing
			// call until it exhausts the turn budget.
			if len(guardGuidance) > 0 {
				a.cli.history = append(a.cli.history, models.Message{
					Role:    "user",
					Content: strings.Join(guardGuidance, "\n"),
				})
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

			// Unattended runs (ACP, MCP agent_task) have no human at the menu:
			// handleCommandBlocks would block forever reading a dead stdin —
			// worse, that stdin is the JSON-RPC channel. Execute the blocks
			// headless (danger gate + optional IDE permission dialog inside)
			// and feed the results back so the ReAct loop keeps going.
			if a.cli.unattended {
				feedback := a.executeCommandBlocksUnattended(ctx, commandBlocks)
				a.cli.history = append(a.cli.history, models.Message{
					Role:    "user",
					Content: i18n.T("agent.feedback.tool_output", commandBlockNamesLabel(commandBlocks), feedback),
				})
				showTurnStats()
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

			// Suspend the raw stdin reader so we can use line-editing input.
			// The raw reader captures escape sequences as literal bytes (^[[A for arrows),
			// making it impossible to navigate text. We temporarily switch to
			// golang.org/x/term which provides full readline support.
			// suspend/resume (not stop/start) so the reader refcount held by
			// this — possibly nested — loop scope stays balanced.
			a.suspendStdinReader()

			userInput, err := a.readLineWithEditing()

			// Restart the stdin reader for subsequent agent turns
			a.resumeStdinReader(ctx)
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

			userInput = a.expandFollowUpCommand(ctx, userInput)
			a.cli.history = append(a.cli.history, models.Message{
				Role:    "user",
				Content: userInput,
			})
			// Same contract as the type-ahead path: a mid-session follow-up
			// activates its skills before the next turn is sent.
			if block, names := a.rescanSkillsMidLoop(userInput, turn); block != "" {
				a.cli.history = append(a.cli.history, models.Message{
					Role:    "user",
					Content: block,
					Meta:    &models.MessageMeta{SkillNames: models.JoinSkillNames(names)},
				})
				notifySkillActivation(names)
			}
			// And proactive recall against the follow-up's own words — the
			// system prompt's recall blocks froze at Run() time.
			if rb := a.followUpRecallBlocks(ctx, userInput); rb != "" {
				// Tagged like every other injected block: excluded from
				// extraction, autosave indexing and session search, so the
				// recall never feeds itself back into memory.
				a.cli.history = append(a.cli.history, models.TurnContextMessage(turnContextHeader+rb))
			}
			continue
		}

		showTurnStats()
		// Gateway/unattended: the clean final reply is the completion signal,
		// so skip the "TAREFA CONCLUÍDA" banner that would otherwise precede it
		// as feed noise. (The max-turns banner below is kept — it's an
		// exceptional outcome the operator should still see.)
		if !a.cli.unattended {
			fmt.Println(renderer.Colorize("\n"+i18n.T("agent.status.task_completed"), agent.ColorGreen+agent.ColorBold))
		}
		a.emitSessionSummary()
		return nil
	}

	fmt.Println(renderer.Colorize(
		"\n"+i18n.T("agent.status.max_turns_stopped", maxTurns),
		agent.ColorYellow,
	))
	a.emitSessionSummary()
	return nil
}

// emitSessionSummary prints a one-line wrap-up of cumulative session spend
// (tokens · cost · compression savings) when an agent/coder run finishes, so
// the user gets the same close-out awareness chat mode gives per reply. The
// figures are session-cumulative (the cost tracker spans the whole CLI
// session, like /cost). No-op when unattended or when nothing was tracked.
func (a *AgentMode) emitSessionSummary() {
	if a.cli.unattended || a.cli.costTracker == nil {
		return
	}
	totalTokens := a.cli.costTracker.TotalTokens()
	totalCost := a.cli.costTracker.TotalCost()
	if totalTokens == 0 && totalCost == 0 {
		return
	}
	parts := []string{formatTokenCount(totalTokens)}
	if totalCost > 0 {
		parts = append(parts, formatTurnCost(totalCost))
	}
	if a.cli.compressionLayer != nil {
		if s, _ := a.cli.compressionLayer.Stats(); s.SavedBytes() > 0 {
			if savedTok := a.cli.estimateTokens64(s.SavedBytes()); savedTok > 0 {
				parts = append(parts, i18n.T("chat.envelope.compression_saved", formatTokenCount(savedTok)))
			}
		}
	}
	fmt.Println(colorize("  "+i18n.T("agent.telemetry.session", strings.Join(parts, " · ")), ColorGray))
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
func (a *AgentMode) initMultiAgent(ctx context.Context) bool {
	modeStr := strings.TrimSpace(strings.ToLower(os.Getenv("CHATCLI_AGENT_PARALLEL_MODE")))
	if modeStr == "false" || modeStr == "0" {
		a.parallelMode = false
		return false
	}

	// Registry already initialized — just update provider/model in case
	// the user switched providers at runtime.
	if a.agentRegistry != nil {
		a.parallelMode = true
		// Re-sync session-scoped policy state: unattended and automode may
		// have changed since the registry was first built (this branch used
		// to freeze the boot-time values for the whole process lifetime).
		if a.policyAdapter != nil {
			a.policyAdapter.unattended = a.cli.unattended
			a.policyAdapter.autoApprove = a.askAutoApproved
		}
		workers.RegisterWorkerContextProvider(a.workerTaskContext)
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

	// Every worker's LLM spend lands in the session cost tracker per call,
	// attributed to the provider+model that served the worker — /cost covers
	// subagents live, and the budget hard stop reaches into in-flight
	// dispatch waves instead of only gating the orchestrator's next turn.
	if a.cli.costTracker != nil {
		tracker := a.cli.costTracker
		a.agentDispatcher.SetUsageRecorder(func(provider, model string, usage *models.UsageInfo) {
			tracker.RecordRealUsage(provider, model, usage)
		})
		a.agentDispatcher.SetBudgetGate(a.cli.budgetBlockedErr)
	}

	// Attach policy enforcement so parallel workers respect security rules
	if pa, err := newWorkerPolicyAdapter(a.logger); err == nil {
		pa.unattended = a.cli.unattended // gateway: auto-approve "ask" instead of blocking on stdin
		pa.autoApprove = a.askAutoApproved
		a.policyAdapter = pa
		a.agentDispatcher.SetPolicyChecker(pa)
		a.logger.Info("Policy enforcement enabled for parallel workers")
	} else {
		a.logger.Warn("Failed to initialize policy checker for parallel workers", zap.Error(err))
	}

	// Proactive recall for workers: each dispatched task gets the same
	// [MEMORY AUTO-RECALL] / [SESSION RECALL] surfaces the orchestrator's
	// system prompt carries, keyed off the task text.
	workers.RegisterWorkerContextProvider(a.workerTaskContext)

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
	enqueuer := a.cli.reflexionEnqueuer(ctx, a.qualityConfig.Reflexion.Queue)
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

// sessionWorkspaceDynamicLine states the session scratch directory's concrete
// path. It belongs to the UNCACHED dynamic block: the path changes on every
// process start, so keeping it out of the stable tools prefix is what lets
// that prefix cache-hit across CLI restarts.
func sessionWorkspaceDynamicLine() string {
	ws := agent.GetSessionWorkspace()
	if ws == nil || ws.ScratchDir == "" {
		return ""
	}
	return "Session scratch directory (CHATCLI_AGENT_TMPDIR): " + ws.ScratchDir
}

// buildMCPToolsSection renders the system-prompt block listing MCP
// tools available this turn, plus the routing hint that biases the
// model toward `mcp_*` when an MCP server covers the requested op.
// Pure function so the rendering can be tested without spinning up an
// AgentMode instance and a live LLM client.
func buildMCPToolsSection(tools []models.ToolDefinition, isCoderMode bool) string {
	var b strings.Builder
	b.WriteString("MCP Tools (external):\n")
	b.WriteString("  Para invocar: <tool_call name=\"mcp_<tool>\" args='{\"param\":\"value\"}' />\n")
	b.WriteString("  Antes do primeiro uso de uma tool MCP, obtenha o schema completo de parâmetros com " +
		"<tool_call name=\"@tools\" args='{\"cmd\":\"describe\",\"args\":{\"name\":\"mcp_<tool>\"}}' /> — não adivinhe argumentos.\n")
	b.WriteString("  Se invocar sem os parâmetros obrigatórios, o sistema retornará o schema para você corrigir.\n")
	if isCoderMode {
		b.WriteString("\n  " + i18n.T("agent.mcp.routing_hint_coder") + "\n")
	} else {
		b.WriteString("\n  " + i18n.T("agent.mcp.routing_hint_agent") + "\n")
	}
	b.WriteString("\n")
	for _, t := range tools {
		b.WriteString(fmt.Sprintf("  - %s: %s\n",
			t.Function.Name, clampIndexDescription(t.Function.Description)))
	}
	return b.String()
}

// mcpIndexDescMaxChars caps each MCP tool's description in the system-prompt
// index. Enterprise MCP servers ship multi-paragraph descriptions; with
// 100+ tools connected they alone add tens of KB to every request — the
// exact payload floor that trips corporate proxy/WAF body caps. The full
// description and schema remain one @tools describe away.
const mcpIndexDescMaxChars = 160

// clampIndexDescription reduces a tool description to its first line,
// whitespace-collapsed and capped at mcpIndexDescMaxChars, rune-safe.
func clampIndexDescription(desc string) string {
	if idx := strings.IndexByte(desc, '\n'); idx >= 0 {
		desc = desc[:idx]
	}
	desc = strings.Join(strings.Fields(desc), " ")
	if len(desc) <= mcpIndexDescMaxChars {
		return desc
	}
	cut := desc[:mcpIndexDescMaxChars]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
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

// buildMCPAuthNote lists servers currently blocked on OAuth authorization and
// instructs the model to run @mcp-login for them. It is appended to the tool
// context in every mode (whether or not other MCP tools are already usable) so
// a server whose tools never loaded — because it demands authorization — is
// still discoverable and actionable to the agent.
func buildMCPAuthNote(statuses []mcp.ServerStatus) string {
	var pending []string
	for _, s := range statuses {
		if s.AuthRequired {
			pending = append(pending, s.Name)
		}
	}
	if len(pending) == 0 {
		return ""
	}
	return "\n" + i18n.T("agent.mcp.note_auth_required", strings.Join(pending, ", ")) + "\n"
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

// buildSessionWorkspaceHint returns a compact prompt block that teaches the
// model how to use the per-session scratch dir and how to recover data from
// truncated tool results. Returns an empty string when the session workspace
// hasn't been initialized (shouldn't happen during normal startup).
func buildSessionWorkspaceHint() string {
	ws := agent.GetSessionWorkspace()
	if ws == nil {
		return ""
	}
	// The concrete path is deliberately NOT embedded here: this block lives
	// in the cacheable tools prefix, and the scratch dir is random per
	// process, so interpolating it guaranteed a cold prompt cache on every
	// new CLI start. The literal path rides in the uncached dynamic block
	// (sessionWorkspaceDynamicLine) instead.
	return "\n\n## SESSION WORKSPACE & LARGE OUTPUTS\n" +
		"\n" +
		"You have an isolated scratch directory for this session, exposed via the\n" +
		"environment variable `CHATCLI_AGENT_TMPDIR` (its current path is stated in the\n" +
		"dynamic context at the end of this prompt).\n" +
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
	// Seed the mid-loop re-scan dedup set: everything injected here is
	// already in the model's system prompt for the whole Run.
	a.noteInjectedSkills(merged...)
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

// estimateToolDefsChars measures the native tool definitions this run ships
// on every request (coder tools, granted plugins, MCP tools). They are not
// part of the history slice, so the compaction budget reserves them
// explicitly instead of discovering the overflow at the provider.
func (a *AgentMode) estimateToolDefsChars() int {
	var defs []models.ToolDefinition
	if a.isCoderMode {
		defs = workers.CoderToolDefinitions(nil)
	}
	defs = append(defs, workers.PluginToolDefinitions()...)
	if a.cli != nil && a.cli.mcpManager != nil && a.cli.mcpManager.ToolCount() > 0 {
		defs = append(defs, a.cli.mcpManager.GetTools()...)
	}
	if len(defs) == 0 {
		return 0
	}
	raw, err := json.Marshal(defs)
	if err != nil {
		return 0
	}
	return len(raw)
}
