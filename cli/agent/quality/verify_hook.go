/*
 * ChatCLI - CoVe PostHook (Phase 6).
 *
 * VerifyHook routes the just-finished worker's draft through the
 * VerifierAgent. When the verifier flags a discrepancy AND
 * RewriteOnDiscrepancy is true, the rewritten <final> block replaces
 * result.Output. The discrepancy state is recorded on
 * result.Metadata so the Reflexion hook (Phase 4) can pick it up.
 */
package quality

import (
	"context"
	"fmt"

	"github.com/diillson/chatcli/cli/agent/workers"
	"go.uber.org/zap"
)

// VerifyHook is the CoVe PostHook.
type VerifyHook struct {
	dispatch DispatchOne
	logger   *zap.Logger
}

// NewVerifyHook constructs a VerifyHook.
func NewVerifyHook(dispatch DispatchOne, logger *zap.Logger) *VerifyHook {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &VerifyHook{dispatch: dispatch, logger: logger}
}

// Name identifies the hook.
func (h *VerifyHook) Name() string { return "verify" }

// PostRun runs CoVe on the worker's output. Skips when:
//   - the worker errored (Reflexion handles failures separately)
//   - cfg.Verify.Enabled is false
//   - the agent type is in cfg.Verify.ExcludeAgents
//   - the draft is empty
func (h *VerifyHook) PostRun(ctx context.Context, hc *HookContext, result *workers.AgentResult) error {
	if h.dispatch == nil || result == nil || result.Error != nil {
		return nil
	}
	cfg := hc.Config.Verify
	if !cfg.Enabled || result.Output == "" {
		return nil
	}
	if !AppliesToAgent(string(hc.Agent.Type()), cfg.ExcludeAgents) {
		return nil
	}

	numQ := cfg.NumQuestions
	if numQ <= 0 {
		numQ = workers.DefaultNumVerificationQuestions
	}

	body := workers.VerifyDirective + fmt.Sprintf(" [NUM_QUESTIONS=%d]\n", numQ) +
		"Task:\n" + hc.Task + "\n\n" +
		"Draft:\n" + result.Output
	call := workers.AgentCall{
		Agent: workers.AgentTypeVerifier,
		Task:  body,
		ID:    "verify",
	}
	res := h.dispatch(ctx, call)
	if res.Error != nil {
		h.logger.Warn("verifier dispatch failed; keeping draft",
			zap.String("source_agent", string(hc.Agent.Type())),
			zap.Error(res.Error))
		return nil
	}

	// Record discrepancy state for Reflexion (Phase 4) to consume.
	// We piggyback on AgentResult by appending to a shared metadata
	// map — added here lazily so the verifier doesn't need to mutate
	// the AgentResult struct shape.
	parsed := workers.ParseVerifierOutput(res.Output)
	if parsed.HasDiscrepancy() {
		result.SetMetadata("verified_with_discrepancy", "true")
		result.SetMetadata("verifier_discrepancies", parsed.Discrepancies)
		if cfg.RewriteOnDiscrepancy && parsed.Final != "" {
			result.Output = parsed.Final
		}
	} else {
		result.SetMetadata("verified_clean", "true")
	}
	return nil
}
