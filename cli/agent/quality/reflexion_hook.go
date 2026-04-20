/*
 * ChatCLI - Reflexion PostHook (Phase 4).
 *
 * Triggered after a worker run when the result hits a quality gate
 * (error, hallucination flagged by VerifyHook, low refine score) or
 * when /reflect explicitly arms reflexion. Generates a structured
 * Lesson via the LLM and persists it to long-term memory so future
 * RAG+HyDE retrievals surface it for similar tasks.
 *
 * Reflexion never blocks the user-facing turn: lesson generation is
 * async (best-effort), and persistence failures are logged.
 */
package quality

import (
	"context"

	"github.com/diillson/chatcli/cli/agent/workers"
	"go.uber.org/zap"
)

// ReflexionHook is the PostHook that materializes lessons.
type ReflexionHook struct {
	llm     LessonLLM
	persist PersistLessonFunc
	logger  *zap.Logger
}

// NewReflexionHook constructs the hook. Either llm or persist nil
// degrades to a no-op (logged on first use).
func NewReflexionHook(llm LessonLLM, persist PersistLessonFunc, logger *zap.Logger) *ReflexionHook {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ReflexionHook{llm: llm, persist: persist, logger: logger}
}

// Name identifies the hook.
func (h *ReflexionHook) Name() string { return "reflexion" }

// PostRun fires when one of the configured triggers matches. It
// schedules a background goroutine for lesson generation so the
// user-facing turn is not slowed by an extra LLM call.
func (h *ReflexionHook) PostRun(ctx context.Context, hc *HookContext, result *workers.AgentResult) error {
	if h.llm == nil || h.persist == nil {
		return nil
	}
	// Respect an already-canceled turn ctx: when the user has
	// ctrl-c'd the whole /agent run, there's no point generating a
	// lesson about it. The goroutine below still uses a fresh ctx
	// (Background) so normal per-turn timeouts don't kill lesson
	// generation mid-flight.
	if err := ctx.Err(); err != nil {
		return nil
	}
	cfg := hc.Config.Reflexion
	if !cfg.Enabled {
		return nil
	}

	trigger := h.detectTrigger(cfg, result)
	if trigger == "" {
		return nil
	}

	req := LessonRequest{
		Task:    hc.Task,
		Attempt: result.Output,
		Outcome: h.formatOutcome(result),
		Trigger: trigger,
	}

	// Background — never block the turn. Use a fresh context so the
	// per-worker timeout doesn't kill the lesson call mid-flight.
	go h.runReflexion(context.Background(), req) //#nosec G118 -- detached on purpose; lesson gen outlives the turn
	return nil
}

func (h *ReflexionHook) runReflexion(ctx context.Context, req LessonRequest) {
	lesson, err := GenerateLesson(ctx, h.llm, req)
	if err != nil {
		h.logger.Warn("reflexion: lesson generation failed",
			zap.String("trigger", req.Trigger),
			zap.Error(err))
		return
	}
	if lesson == nil {
		// Model declared "no actionable lesson" — that's a valid
		// outcome, no error, no persistence.
		return
	}
	if err := h.persist(ctx, *lesson); err != nil {
		h.logger.Warn("reflexion: persist failed",
			zap.String("trigger", req.Trigger),
			zap.Error(err))
		return
	}
	h.logger.Info("reflexion: lesson persisted",
		zap.String("trigger", req.Trigger),
		zap.String("situation", lesson.Situation),
		zap.Strings("tags", lesson.Tags))
}

// detectTrigger returns the trigger label or "" when no configured
// gate matched. Manual triggering ("manual") is set externally on
// result.Metadata by /reflect before the hook runs.
func (h *ReflexionHook) detectTrigger(cfg ReflexionConfig, result *workers.AgentResult) string {
	if result.MetadataFlag(MetaForceReflexion) {
		return "manual"
	}
	if cfg.OnError && result.Error != nil {
		return "error"
	}
	if cfg.OnHallucination && result.MetadataFlag("verified_with_discrepancy") {
		return "hallucination"
	}
	if cfg.OnLowQuality && result.MetadataFlag("refine_low_quality") {
		return "low_quality"
	}
	return ""
}

// formatOutcome assembles the "why are we reflecting" text the
// generator sees. Errors and discrepancy reports get prepended so
// the LLM has the diagnostic to work with.
func (h *ReflexionHook) formatOutcome(result *workers.AgentResult) string {
	switch {
	case result.Error != nil:
		return "ERROR: " + result.Error.Error()
	case result.MetadataFlag("verified_with_discrepancy"):
		return "DISCREPANCY (from CoVe): " + result.Metadata["verifier_discrepancies"]
	case result.MetadataFlag("refine_low_quality"):
		return "LOW QUALITY (from Self-Refine): " + result.Metadata["refine_low_quality_reason"]
	default:
		return "MANUAL: user requested /reflect"
	}
}

// MetaForceReflexion is the metadata key /reflect sets to force
// reflexion regardless of the configured triggers. Exported so the
// command handler can write it onto a result.
const MetaForceReflexion = "force_reflexion"
