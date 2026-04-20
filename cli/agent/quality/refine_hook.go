/*
 * ChatCLI - Self-Refine PostHook (Phase 5).
 *
 * RefineHook wraps the just-finished worker's draft and routes it
 * through the RefinerAgent for one or more critique-rewrite passes.
 * Convergence (rewrite ≈ draft) interrupts the loop early so we
 * don't spend tokens iterating over an already-good answer.
 */
package quality

import (
	"context"

	"github.com/diillson/chatcli/cli/agent/workers"
	"go.uber.org/zap"
)

// RefineHook is the PostHook that runs Self-Refine on a worker's
// output. It is opt-in via cfg.Refine.Enabled and skipped for any
// agent in cfg.Refine.ExcludeAgents (Formatter, Deps by default).
//
// The refiner is dispatched via a small DispatchOne callback so this
// hook can drive both the live workers.Dispatcher (production) and a
// stub dispatcher (tests).
type RefineHook struct {
	dispatch DispatchOne
	logger   *zap.Logger
}

// DispatchOne dispatches a single AgentCall and returns its result.
// Used by the quality hooks to route refine/verify calls without
// importing workers.Dispatcher transitively (keeps tests cheap).
type DispatchOne func(ctx context.Context, call workers.AgentCall) workers.AgentResult

// NewRefineHook constructs a RefineHook. nil logger upgrades to a no-op.
func NewRefineHook(dispatch DispatchOne, logger *zap.Logger) *RefineHook {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RefineHook{dispatch: dispatch, logger: logger}
}

// Name identifies the hook for logs and exclude lists.
func (h *RefineHook) Name() string { return "refine" }

// PostRun runs Self-Refine on result.Output when:
//   - the worker did not error (errors are Reflexion territory)
//   - cfg.Refine.Enabled is true
//   - the agent type is not in cfg.Refine.ExcludeAgents
//   - draft is non-trivially long (>= MinDraftBytes) — short outputs
//     are usually status lines that don't benefit
//
// Up to MaxPasses iterations of {critique → revise} are run. The
// loop stops early when the next revision changes by fewer than
// EpsilonChars (convergence).
func (h *RefineHook) PostRun(ctx context.Context, hc *HookContext, result *workers.AgentResult) error {
	if h.dispatch == nil || result == nil || result.Error != nil {
		return nil
	}
	cfg := hc.Config.Refine
	if !cfg.Enabled {
		return nil
	}
	if !AppliesToAgent(string(hc.Agent.Type()), cfg.ExcludeAgents) {
		return nil
	}
	draft := result.Output
	if len(draft) < cfg.MinDraftBytes {
		return nil
	}

	maxPasses := cfg.MaxPasses
	if maxPasses <= 0 {
		maxPasses = 1
	}
	originalTask := hc.Task
	currentDraft := draft

	for pass := 0; pass < maxPasses; pass++ {
		body := workers.RefineDirective + "\n" +
			"Task:\n" + originalTask + "\n\n" +
			"Draft:\n" + currentDraft
		call := workers.AgentCall{
			Agent: workers.AgentTypeRefiner,
			Task:  body,
			ID:    "refine-pass",
		}
		res := h.dispatch(ctx, call)
		if res.Error != nil {
			h.logger.Warn("refine pass failed; keeping previous draft",
				zap.String("source_agent", string(hc.Agent.Type())),
				zap.Int("pass", pass),
				zap.Error(res.Error))
			break
		}
		next := res.Output
		if convergedRefine(currentDraft, next, cfg.EpsilonChars) {
			currentDraft = next
			break
		}
		currentDraft = next
	}

	if currentDraft != draft {
		result.Output = currentDraft
	}
	return nil
}

// convergedRefine reports whether two consecutive revisions are
// "close enough" to stop iterating. Uses a simple absolute-length
// delta + character-level Levenshtein-bounded heuristic to avoid
// pulling in a heavy diff library — for refine, "almost identical"
// is the right granularity.
func convergedRefine(a, b string, epsilon int) bool {
	if epsilon <= 0 {
		epsilon = 50
	}
	delta := len(a) - len(b)
	if delta < 0 {
		delta = -delta
	}
	if delta > epsilon {
		return false
	}
	// Cheap upper-bound: count character mismatches up to min length.
	mismatch := 0
	la := []rune(a)
	lb := []rune(b)
	n := len(la)
	if len(lb) < n {
		n = len(lb)
	}
	for i := 0; i < n; i++ {
		if la[i] != lb[i] {
			mismatch++
			if mismatch > epsilon {
				return false
			}
		}
	}
	return true
}
