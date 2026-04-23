/*
 * ChatCLI - Reflexion PostHook (Phase 4).
 *
 * Triggered after a worker run when the result hits a quality gate
 * (error, hallucination flagged by VerifyHook, low refine score) or
 * when /reflect explicitly arms reflexion. Produces a structured
 * Lesson via the LLM and persists it to long-term memory so future
 * RAG+HyDE retrievals surface it for similar tasks.
 *
 * Reflexion never blocks the user-facing turn. Two delivery modes:
 *
 *   1. Durable queue (preferred, opt-out): the hook hands the
 *      LessonRequest to a lessonq.Enqueuer which writes a WAL record
 *      synchronously and returns. A worker pool picks the job up
 *      asynchronously, retries transient failures with exponential
 *      backoff, and moves hard failures to a DLQ. Process crashes
 *      between trigger and persist are recovered via WAL replay on
 *      the next boot.
 *
 *   2. Legacy detached goroutine (fallback when no enqueuer is wired):
 *      the hook fires `go runReflexion(...)` exactly as before. This
 *      preserves the original behavior for tests and for users who
 *      disable the queue via config.
 */
package quality

import (
	"context"

	"github.com/diillson/chatcli/cli/agent/workers"
	"go.uber.org/zap"
)

// LessonEnqueuer is the minimal contract the hook needs to hand a
// request off to a durable queue. The lessonq package implements this
// on its Runner; tests can stub it freely.
type LessonEnqueuer interface {
	// Enqueue accepts a LessonRequest for durable processing. Returns
	// nil on accept (including idempotent re-submit). Errors are
	// logged by the hook but never propagated up — reflexion is
	// best-effort from the turn's perspective.
	Enqueue(ctx context.Context, req LessonRequest) error
}

// ReflexionHook is the PostHook that materializes lessons.
type ReflexionHook struct {
	llm       LessonLLM
	persist   PersistLessonFunc
	enqueuer  LessonEnqueuer
	logger    *zap.Logger
}

// NewReflexionHook constructs the hook with the legacy detached-
// goroutine path (no durable queue). Either llm or persist nil
// degrades to a no-op.
//
// Kept for backward compatibility with callers that don't (yet) wire
// a lessonq.Runner. New callers should use NewReflexionHookWithQueue.
func NewReflexionHook(llm LessonLLM, persist PersistLessonFunc, logger *zap.Logger) *ReflexionHook {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ReflexionHook{llm: llm, persist: persist, logger: logger}
}

// NewReflexionHookWithQueue returns a hook that routes every trigger
// through the provided enqueuer. llm and persist are still needed so
// a future Shutdown → next-boot replay can hand the Runner the same
// callbacks; pass the same ones used in the Runner's Processor.
//
// When enqueuer is nil this behaves identically to NewReflexionHook
// (legacy detached-goroutine mode).
func NewReflexionHookWithQueue(enqueuer LessonEnqueuer, llm LessonLLM, persist PersistLessonFunc, logger *zap.Logger) *ReflexionHook {
	h := NewReflexionHook(llm, persist, logger)
	h.enqueuer = enqueuer
	return h
}

// Name identifies the hook.
func (h *ReflexionHook) Name() string { return "reflexion" }

// PostRun fires when one of the configured triggers matches. In queue
// mode it writes a WAL record and returns; in legacy mode it spawns a
// detached goroutine. Never blocks the user-facing turn.
func (h *ReflexionHook) PostRun(ctx context.Context, hc *HookContext, result *workers.AgentResult) error {
	// If enqueuer isn't wired AND the legacy callbacks are nil, nothing
	// we can do — silently skip.
	if h.enqueuer == nil && (h.llm == nil || h.persist == nil) {
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

	if h.enqueuer != nil {
		// Durable path: synchronous WAL write inside Enqueue, then
		// return. The enqueue call is sub-millisecond in practice
		// (single fsync + rename), well under the user-noticeable
		// latency budget for a turn.
		if err := h.enqueuer.Enqueue(context.Background(), req); err != nil {
			h.logger.Warn("reflexion: enqueue failed; lesson may be lost",
				zap.String("trigger", trigger),
				zap.Error(err))
		}
		return nil
	}

	// Legacy path — kept verbatim from the pre-queue behavior so
	// turning the queue off is a zero-regression operation.
	//
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
