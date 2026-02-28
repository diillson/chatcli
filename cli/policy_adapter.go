/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
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

// pauseSpinner stops the spinner output and clears the line.
func (a *workerPolicyAdapter) pauseSpinner() {
	if a.timer != nil {
		a.timer.Pause()
		fmt.Print(metrics.ClearLine())
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
		a.pm, _ = coder.NewPolicyManager(a.logger) // reload rules
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
		decision := coder.PromptSecurityCheckWithContext(ctx, toolName, args, secCtx)
		pattern := coder.GetSuggestedPattern(toolName, args)

		// Clear the prompt area and resume spinner
		fmt.Print(metrics.ClearLine())
		a.resumeSpinner()

		switch decision {
		case coder.DecisionAllowAlways:
			if pattern != "" {
				_ = a.pm.AddRule(pattern, coder.ActionAllow)
			}
			return true, ""

		case coder.DecisionDenyForever:
			if pattern != "" {
				_ = a.pm.AddRule(pattern, coder.ActionDeny)
			}
			return false, "AÇÃO BLOQUEADA PERMANENTEMENTE. NÃO TENTE NOVAMENTE."

		case coder.DecisionDenyOnce:
			return false, "AÇÃO NEGADA PELO USUÁRIO. NÃO TENTE O MESMO COMANDO NOVAMENTE."

		default: // DecisionRunOnce
			return true, ""
		}
	}

	// Fallback: unknown action → deny (safe default)
	return false, "AÇÃO BLOQUEADA (política desconhecida)."
}
