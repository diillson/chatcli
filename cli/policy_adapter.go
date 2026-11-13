/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/metrics"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// workerPolicyAdapter wraps coder.PolicyManager to implement
// workers.PolicyChecker. It serializes interactive "ask" prompts with a mutex
// so that only one parallel worker blocks on stdin at a time.
// It also pauses/resumes the spinner so the prompt renders cleanly.
type workerPolicyAdapter struct {
	pm     *coder.PolicyManager
	logger *zap.Logger
	mu     sync.Mutex // serializes interactive security prompts

	// spinner control — set after creation via setSpinner
	timer *metrics.Timer

	// stdinCh is the centralized stdin channel from AgentMode.
	// When set, security prompts read from this channel instead of spawning
	// a goroutine on os.Stdin, avoiding orphaned readers after Ctrl+C.
	stdinCh <-chan string

	// liveStdin is the agent loop's stdin channel while its reader runs, nil
	// otherwise, with the channel that reader's teardown closes. A detached
	// run asks through it: stdinCh outlives the turn.
	liveStdin func() (<-chan string, <-chan struct{})

	// detachedPrompt, when set, is told that a detached run's prompt is
	// about to read the turn's stdin lines; the func it returns is called
	// when the prompt is over.
	detachedPrompt func() (closed func())

	// unattended marks a run with no human to answer an "ask" prompt
	// (gateway, server, RPC surfaces): PromptSecurityCheck would block on a
	// dead stdin forever, so "ask" is refused unless autoApprove admits it
	// (the operator's policy automode opt-in). ActionDeny always blocks.
	unattended bool

	// autoApprove, when set, is consulted on an "ask" verdict before any
	// interactive prompt: it reports whether the session's /policy automode
	// admits this call (safety-immune operations excluded — see
	// AgentMode.askAutoApproved). Without it, automode only reached the
	// orchestrator's own tool calls and workers kept prompting on stdin.
	autoApprove func(toolName, args string) bool

	// restoreInput, when set, runs after each interactive prompt so the
	// agent loop can re-arm its cbreak type-ahead TTY state (the prompt
	// forces cooked mode to render/read cleanly).
	restoreInput func()
}

// newWorkerPolicyAdapter creates a PolicyChecker backed by coder.PolicyManager.
func newWorkerPolicyAdapter(logger *zap.Logger) (*workerPolicyAdapter, error) {
	pm, err := coder.NewPolicyManager(logger)
	if err != nil {
		return nil, err
	}
	return &workerPolicyAdapter{pm: pm, logger: logger}, nil
}

// setSpinner attaches the timer so the adapter can pause/resume the spinner
// around interactive prompts.
func (a *workerPolicyAdapter) setSpinner(t *metrics.Timer) {
	a.timer = t
}

// setStdinCh sets the centralized stdin channel for security prompts.
// setRestoreInput registers a callback invoked after each interactive
// prompt finishes (the prompt forces cooked mode; the agent loop re-arms
// its cbreak type-ahead state through this hook).
func (a *workerPolicyAdapter) setRestoreInput(fn func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.restoreInput = fn
}

func (a *workerPolicyAdapter) setStdinCh(ch <-chan string) {
	a.stdinCh = ch
}

// liveStdinLines is the stdin channel while the reader goroutine runs, with
// the channel that same reader's teardown closes.
func (a *AgentMode) liveStdinLines() (<-chan string, <-chan struct{}) {
	a.stdinMu.Lock()
	defer a.stdinMu.Unlock()
	if a.stdinLines == nil {
		return nil, nil
	}
	return a.stdinLines, a.stdinDone
}

// noteDetachedPrompt records that a permission prompt of a detached run is
// reading this loop's stdin lines, until the returned func is called. A
// teardown of the reader under such a prompt must not take a line left on
// the channel for type-ahead: it was typed as the answer.
func (a *AgentMode) noteDetachedPrompt() (closed func()) {
	a.detachedPrompts.Add(1)
	return func() { a.detachedPrompts.Add(-1) }
}

// parkWithdrawnPromptLines takes the lines a teardown found on the channel
// while a detached permission prompt was open, and returns the draft to
// carry: those lines, then the half-typed one. They are neither queued for
// the model (a "y" would reach it as an instruction) nor dropped.
func (a *AgentMode) parkWithdrawnPromptLines(lines []string, preview string) string {
	var kept []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	a.withdrawnPromptLines = kept
	if preview != "" {
		kept = append(append([]string(nil), kept...), preview)
	}
	return strings.Join(kept, " ")
}

// announceWithdrawnPrompt tells the user, once, what a withdrawn prompt
// left unsent. Called at the end of the teardown, with the terminal back
// in cooked mode.
func (a *AgentMode) announceWithdrawnPrompt() {
	lines := a.withdrawnPromptLines
	a.withdrawnPromptLines = nil
	if len(lines) == 0 || (a.cli != nil && a.cli.unattended) {
		return
	}
	fmt.Printf("\n  %s\n", colorize("⚠ "+i18n.T("leftovers.prompt.withdrawn"), ColorYellow))
	for _, line := range lines {
		fmt.Printf("    %s\n", colorize("❯ "+sanitizeTypeaheadPreview(line), ColorGray))
	}
}

// chanClosed reports whether ch is closed; a nil channel never is.
func chanClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// promptWhileReaderLives runs ask, ending it as soon as readerGone closes.
// A detached run's prompt reads the turn's stdin channel; once the turn
// tears its reader down nothing feeds it again, and the prompt held the
// worker until its timeout. abandoned: it ended that way, not by an answer.
func promptWhileReaderLives(ctx context.Context, readerGone <-chan struct{}, ask func(context.Context) coder.SecurityDecision) (decision coder.SecurityDecision, abandoned bool) {
	if readerGone == nil {
		return ask(ctx), false
	}
	promptCtx, cancel := context.WithCancel(ctx)
	var left atomic.Bool
	watcher := make(chan struct{})
	go func() {
		defer close(watcher)
		select {
		case <-readerGone:
			left.Store(true)
			cancel()
		case <-promptCtx.Done():
		}
	}()
	decision = ask(promptCtx)
	cancel()
	<-watcher
	// An answer given as the reader left is still the user's answer.
	return decision, left.Load() && decision == coder.DecisionCanceled
}

// pauseSpinner stops the spinner output. The Timer.onPause callback
// (registered in agent_mode.go) clears the multi-line progress display.
func (a *workerPolicyAdapter) pauseSpinner() {
	if a.timer != nil {
		a.timer.Pause()
	}
}

// resumeSpinner restores the spinner output.
func (a *workerPolicyAdapter) resumeSpinner() {
	if a.timer != nil {
		a.timer.Resume()
	}
}

// withPromptGate runs fn serialized on the same mutex the interactive
// security prompts hold for their whole render-and-read lifetime. Live
// stream output printed through it can never interleave with a prompt box
// or pollute the line the user is typing the answer on; while a prompt is
// on screen the printing goroutine simply waits.
func (a *workerPolicyAdapter) withPromptGate(fn func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fn()
}

// tryPromptGate is the non-blocking variant for callers that MUST NOT wait
// on the gate — the stdin reader goroutine above all: a security prompt
// holds the mutex while waiting for a line that only that goroutine can
// deliver, so blocking there would deadlock the prompt forever. Returns
// false (fn not run) when the gate is contended.
func (a *workerPolicyAdapter) tryPromptGate(fn func()) bool {
	if !a.mu.TryLock() {
		return false
	}
	defer a.mu.Unlock()
	fn()
	return true
}

// buildSecurityContext extracts agent metadata from the context to provide
// rich information in security prompts.
func buildSecurityContext(ctx context.Context) *coder.SecurityContext {
	agentName, _ := ctx.Value(workers.CtxKeyAgentName).(string)
	agentTask, _ := ctx.Value(workers.CtxKeyAgentTask).(string)

	if agentName == "" && agentTask == "" {
		return nil
	}
	return &coder.SecurityContext{
		AgentName: agentName,
		TaskDesc:  agentTask,
	}
}

// CheckAndPrompt checks the policy for a tool call. If the policy is "ask",
// it acquires a mutex, pauses the spinner, and prompts the user interactively
// with full context about which agent is requesting the action and why.
func (a *workerPolicyAdapter) CheckAndPrompt(ctx context.Context, toolName, args string) (bool, string) {
	action := a.pm.Check(toolName, args)

	switch action {
	case coder.ActionAllow:
		return true, ""

	case coder.ActionDeny:
		a.logger.Info("Worker tool call blocked by policy (deny)",
			zap.String("tool", toolName),
		)
		return false, "AÇÃO BLOQUEADA (Regra de Segurança). NÃO TENTE NOVAMENTE."

	case coder.ActionAsk:
		if a.autoApprove != nil && a.autoApprove(toolName, args) {
			// Session automode (/policy mode auto): same convenience the
			// orchestrator's loop gets, with the same safety-immunity carve-out.
			a.logger.Info("Worker tool call auto-approved (policy automode)",
				zap.String("tool", toolName))
			return true, ""
		}

		// Unattended (gateway, server, RPC): nobody can answer, and blocking
		// on stdin would hang the worker. Without the automode opt-in above,
		// "ask" is refused.
		// A detached task-graph run is the same case, unless the agent turn
		// that started it is reading stdin NOW (else nobody feeds the prompt).
		stdinCh, nobody := a.stdinCh, a.unattended
		var readerGone <-chan struct{}
		detached, fromTurn := plugins.TaskGraphDetached(ctx)
		asksTurn := detached && fromTurn && a.liveStdin != nil
		if detached {
			if stdinCh = nil; asksTurn {
				stdinCh, readerGone = a.liveStdin()
			}
			nobody = nobody || stdinCh == nil
		}
		if nobody {
			a.logger.Info("Worker tool call denied (unattended, no automode opt-in)",
				zap.String("tool", toolName))
			return false, i18n.T("agent.policy.unattended_denied", toolName)
		}

		// No terminal (scripted one-shot, CI, eval): the prompt could never
		// be answered and would hold the worker until its timeout.
		if denied, msg := workerNoTerminalDenial(toolName); denied {
			a.logger.Info("Worker tool call denied (no terminal to confirm)", zap.String("tool", toolName))
			return false, msg
		}

		// Serialize prompts: only one worker prompts the user at a time.
		a.mu.Lock()
		defer a.mu.Unlock()

		// Re-check after acquiring the lock — another worker's prompt may
		// have created an "allow always" or "deny forever" rule for this
		// same pattern while we were waiting.
		newPM, pmErr := coder.NewPolicyManager(a.logger) // reload rules
		if pmErr != nil {
			a.logger.Error("failed to reload policy manager", zap.Error(pmErr))
			return false, "AÇÃO BLOQUEADA (erro ao recarregar políticas de segurança)."
		}
		a.pm = newPM
		recheck := a.pm.Check(toolName, args)
		if recheck == coder.ActionAllow {
			return true, ""
		}
		if recheck == coder.ActionDeny {
			return false, "AÇÃO BLOQUEADA (Regra de Segurança). NÃO TENTE NOVAMENTE."
		}

		// This call may have queued behind another worker's prompt for a
		// long time. The reader seen before queueing may have been torn
		// down since — and replaced: a suspend for an overlay resumes with
		// new channels. Ask the reader that is live NOW; with none, do not
		// draw a box nobody can answer.
		if asksTurn {
			stdinCh, readerGone = a.liveStdin()
		}
		if (asksTurn && stdinCh == nil) || chanClosed(readerGone) {
			a.logger.Info("Worker tool call denied (the turn's stdin reader went away before the prompt)",
				zap.String("tool", toolName))
			return false, i18n.T("agent.policy.unattended_denied", toolName)
		}

		// Pause spinner so the prompt renders cleanly
		a.pauseSpinner()

		// Build context for the enhanced prompt
		secCtx := buildSecurityContext(ctx)

		// Prompt the user with full context
		if asksTurn && a.detachedPrompt != nil {
			defer a.detachedPrompt()()
		}
		decision, abandoned := promptWhileReaderLives(ctx, readerGone, func(promptCtx context.Context) coder.SecurityDecision {
			return coder.PromptSecurityCheckWithContextGuarded(promptCtx, toolName, args, secCtx, stdinCh)
		})
		// Clear the prompt area and resume spinner
		fmt.Print(metrics.ClearLine())
		a.resumeSpinner()
		if a.restoreInput != nil {
			a.restoreInput()
		}
		if abandoned {
			// The turn ended between the liveness check and the answer:
			// nobody is left to ask, which is the unattended case.
			a.logger.Info("Worker tool call denied (the turn's stdin reader went away before an answer)",
				zap.String("tool", toolName))
			return false, i18n.T("agent.policy.unattended_denied", toolName)
		}

		return a.applyDecision(decision, toolName, args)
	}

	// Fallback: unknown action → deny (safe default)
	return false, "AÇÃO BLOQUEADA (política desconhecida)."
}

// applyDecision turns the user's answer into the verdict, persisting the
// rule an "always" or "never" answer creates.
func (a *workerPolicyAdapter) applyDecision(decision coder.SecurityDecision, toolName, args string) (bool, string) {
	pattern := coder.GetSuggestedPattern(toolName, args)
	switch decision {
	case coder.DecisionAllowAlways:
		if pattern != "" {
			if err := a.pm.AddRule(pattern, coder.ActionAllow); err != nil {
				a.logger.Warn("failed to persist allow rule", zap.String("pattern", pattern), zap.Error(err))
			}
		}
		return true, ""

	case coder.DecisionDenyForever:
		if pattern != "" {
			if err := a.pm.AddRule(pattern, coder.ActionDeny); err != nil {
				a.logger.Warn("failed to persist deny rule", zap.String("pattern", pattern), zap.Error(err))
			}
		}
		return false, "AÇÃO BLOQUEADA PERMANENTEMENTE. NÃO TENTE NOVAMENTE."

	case coder.DecisionDenyOnce:
		return false, "AÇÃO NEGADA PELO USUÁRIO DESTA VEZ. Tente uma abordagem diferente ou pergunte ao usuário."

	case coder.DecisionCanceled:
		return false, "OPERAÇÃO CANCELADA PELO USUÁRIO (Ctrl+C). Pode tentar a mesma ação novamente se necessário."

	default: // DecisionRunOnce
		return true, ""
	}
}
