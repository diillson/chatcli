/*
 * ChatCLI - Lesson Queue: default Processor adapter.
 *
 * Converts a quality.LessonLLM + quality.PersistLessonFunc pair into
 * a Processor the Runner can call. Classifies errors:
 *
 *   - ctx cancellation / timeout     → Transient (provider likely OK)
 *   - LLM returned skip sentinel     → Skipped (valid terminal state)
 *   - LLM returned "no blocks"       → Permanent (parser failure)
 *   - LLM network-ish errors         → Transient (retry with backoff)
 *   - Persist errors                 → Transient (disk/memory temp)
 *
 * Classification is intentionally tolerant — we prefer a retry over a
 * DLQ for ambiguous failures because the DLQ needs operator action.
 */
package lessonq

import (
	"context"
	"errors"
	"strings"

	"github.com/diillson/chatcli/cli/agent/quality"
	"go.uber.org/zap"
)

// NewProcessor builds a Processor that generates a lesson via llm and
// persists it via persist. Either being nil returns a Processor that
// always classifies jobs as permanent (so they flush to DLQ fast
// without looping).
func NewProcessor(llm quality.LessonLLM, persist quality.PersistLessonFunc, metrics *Metrics, logger *zap.Logger) Processor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(ctx context.Context, job LessonJob) ProcessResult {
		if llm == nil || persist == nil {
			return ProcessResult{
				Outcome: OutcomePermanent,
				Err:     errors.New("lessonq processor: llm or persist nil"),
			}
		}
		// Generate the lesson (the existing quality logic handles
		// the XML-ish parsing + skip sentinel).
		lesson, err := quality.GenerateLesson(ctx, llm, job.Request)
		if err != nil {
			return classifyLLMErr(err)
		}
		if lesson == nil {
			// Model declared "nothing actionable" — valid outcome.
			return ProcessResult{Outcome: OutcomeSkipped}
		}
		if err := persist(ctx, *lesson); err != nil {
			if metrics != nil {
				metrics.PersistFailures.Inc()
			}
			// Persist errors are almost always transient (fs full,
			// memory manager locked, etc.). Let the retry policy
			// decide when to give up.
			logger.Debug("lessonq processor: persist failed",
				zap.String("job_id", string(job.ID)), zap.Error(err))
			return ProcessResult{Outcome: OutcomeTransient, Err: err}
		}
		return ProcessResult{Outcome: OutcomeSuccess}
	}
}

// classifyLLMErr tags ctx-like errors as transient (provider may be
// fine next time), parser-like errors as permanent, and everything
// else as transient by default.
func classifyLLMErr(err error) ProcessResult {
	if err == nil {
		return ProcessResult{Outcome: OutcomeSuccess}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ProcessResult{Outcome: OutcomeTransient, Err: err}
	}
	msg := strings.ToLower(err.Error())
	// The quality parser emits these phrases verbatim when the model
	// returned malformed XML. Retrying won't fix a parser bug, so
	// DLQ is the right move.
	if strings.Contains(msg, "no recognized blocks") ||
		strings.Contains(msg, "missing required blocks") ||
		strings.Contains(msg, "unsupported record version") {
		return ProcessResult{Outcome: OutcomePermanent, Err: err}
	}
	// Everything else → treat as transient. Workers bound this via
	// MaxAttempts so a truly unrecoverable error still terminates.
	return ProcessResult{Outcome: OutcomeTransient, Err: err}
}
