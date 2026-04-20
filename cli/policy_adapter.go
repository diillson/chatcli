/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"fmt"
	"sync"

	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/metrics"
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
func (a *workerPolicyAdapter) setStdinCh(ch <-chan string) {
	a.stdinCh = ch
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

		// Pause spinner so the prompt renders cleanly
		a.pauseSpinner()

		// Build context for the enhanced prompt
		secCtx := buildSecurityContext(ctx)

		// Prompt the user with full context
		decision := coder.PromptSecurityCheckWithContext(ctx, toolName, args, secCtx, a.stdinCh)
		pattern := coder.GetSuggestedPattern(toolName, args)

		// Clear the prompt area and resume spinner
		fmt.Print(metrics.ClearLine())
		a.resumeSpinner()

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

	// Fallback: unknown action → deny (safe default)
	return false, "AÇÃO BLOQUEADA (política desconhecida)."
}
