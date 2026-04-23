/*
 * ChatCLI - Self-Refine PostHook (Phase 5).
 *
 * RefineHook wraps the just-finished worker's draft and routes it
 * through the RefinerAgent for one or more critique-rewrite passes.
 * Convergence is detected by a pluggable Convergence.Check callback
 * (the enterprise Composite cascade: char → jaccard → embedding →
 * optional LLM-judge) so semantic equivalence is recognized — a
 * rewrite that says the same thing in different words is a stop
 * signal, not a reason to keep burning tokens.
 *
 * Quality regression guard: after each pass, the hook compares the
 * new revision against the ORIGINAL draft under the same cascade.
 * If the comparison similarity decreases between passes (i.e. we're
 * drifting away from a decent starting point), the hook reverts to
 * the best draft seen and stops the loop. Metadata flag
 * "refine_rolled_back" is set so Reflexion can note the pattern.
 */
package quality

import (
	"context"

	"github.com/diillson/chatcli/cli/agent/quality/convergence"
	"github.com/diillson/chatcli/cli/agent/workers"
	"go.uber.org/zap"
)

// ConvergenceChecker is the abstract convergence signal. The
// enterprise default is *convergence.Composite; tests can substitute
// a stub that returns deterministic decisions.
type ConvergenceChecker interface {
	Check(ctx context.Context, a, b string) (convergence.CheckResult, error)
}

// RefineHook is the PostHook that runs Self-Refine on a worker's
// output. Opt-in via cfg.Refine.Enabled; skipped for any agent in
// cfg.Refine.ExcludeAgents (Formatter, Deps by default).
type RefineHook struct {
	dispatch    DispatchOne
	logger      *zap.Logger
	convergence ConvergenceChecker // nil → char-only fallback
}

// DispatchOne dispatches a single AgentCall and returns its result.
// Used by the quality hooks to route refine/verify calls without
// importing workers.Dispatcher transitively (keeps tests cheap).
type DispatchOne func(ctx context.Context, call workers.AgentCall) workers.AgentResult

// NewRefineHook constructs a RefineHook with the default (char-only)
// convergence signal. Kept for backward compatibility with existing
// callers; new code should prefer NewRefineHookWithConvergence.
func NewRefineHook(dispatch DispatchOne, logger *zap.Logger) *RefineHook {
	return NewRefineHookWithConvergence(dispatch, nil, logger)
}

// NewRefineHookWithConvergence wires a semantic convergence checker
// (typically *convergence.Composite) into the refine loop. When
// checker is nil, the hook falls back to the legacy char-level
// heuristic for drop-in backward compatibility.
func NewRefineHookWithConvergence(dispatch DispatchOne, checker ConvergenceChecker, logger *zap.Logger) *RefineHook {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RefineHook{dispatch: dispatch, logger: logger, convergence: checker}
}

// Name identifies the hook for logs and exclude lists.
func (h *RefineHook) Name() string { return "refine" }

// PostRun runs Self-Refine on result.Output when:
//   - the worker did not error (errors are Reflexion territory)
//   - cfg.Refine.Enabled is true
//   - the agent type is not in cfg.Refine.ExcludeAgents
//   - draft is non-trivially long (>= MinDraftBytes)
//
// Up to MaxPasses iterations of {critique → revise} are run. The
// loop stops early on:
//   - convergence (new draft ≈ old draft under the configured checker)
//   - quality regression (new draft drifted further from original)
//   - passthrough budget exhaustion
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
	originalDraft := draft
	currentDraft := draft
	bestDraft := draft
	bestSimToOrig := 1.0 // original is trivially 100% similar to itself

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

		// Quality regression guard: how does the new rewrite compare
		// to the ORIGINAL draft? If it's drifting away from a decent
		// starting point, keep the best we've seen and stop.
		simToOrig := 1.0
		if h.convergence != nil {
			cr, err := h.convergence.Check(ctx, originalDraft, next)
			if err == nil {
				simToOrig = cr.Score.Similarity
			}
		}
		if simToOrig > bestSimToOrig || pass == 0 {
			bestSimToOrig = simToOrig
			bestDraft = next
		} else if simToOrig < bestSimToOrig*0.85 {
			// Sharp quality drop (>15% similarity loss vs best) —
			// treat as regression. Revert + stop.
			h.logger.Info("refine: regression detected, reverting to best draft",
				zap.Int("pass", pass),
				zap.Float64("sim_best", bestSimToOrig),
				zap.Float64("sim_now", simToOrig))
			result.SetMetadata("refine_rolled_back", "true")
			currentDraft = bestDraft
			break
		}

		// Convergence: did we stabilize?
		if h.hasConverged(ctx, currentDraft, next, cfg) {
			currentDraft = next
			if simToOrig > bestSimToOrig {
				bestDraft = next
			}
			break
		}
		currentDraft = next
	}

	final := bestDraft
	// Prefer the last draft only when we didn't roll back — i.e.
	// we've been monotonically improving or converged forward.
	if !result.MetadataFlag("refine_rolled_back") && currentDraft != "" {
		final = currentDraft
	}
	if final != draft {
		result.Output = final
	}
	return nil
}

// hasConverged returns true when the new revision is close enough to
// the current draft to stop iterating. Uses the injected checker
// when available; falls back to the legacy char-level heuristic so
// the hook stays functional even when wiring omits a checker.
func (h *RefineHook) hasConverged(ctx context.Context, cur, next string, cfg RefineConfig) bool {
	if h.convergence != nil {
		cr, err := h.convergence.Check(ctx, cur, next)
		if err == nil {
			return cr.Converged
		}
		// On checker error, fall through to the char heuristic so
		// refine doesn't run forever on a flaky scorer.
	}
	return convergedCharHeuristic(cur, next, cfg.EpsilonChars)
}

// convergedCharHeuristic is the original char-level stop-signal,
// preserved as a last-resort fallback. Same semantics as the previous
// convergedRefine in case the composite cascade isn't wired.
func convergedCharHeuristic(a, b string, epsilon int) bool {
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
