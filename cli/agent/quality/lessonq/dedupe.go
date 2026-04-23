/*
 * ChatCLI - Lesson Queue: idempotency key derivation.
 *
 * Idempotency is enforced via a content-addressable JobID. Re-
 * enqueueing the same (task, trigger, attempt) combination yields
 * the same JobID and is treated as a duplicate by the queue.
 *
 * The hash inputs are normalized (trimmed + lowercase for free-text
 * fields) so minor whitespace churn between triggers doesn't inflate
 * the DLQ with near-duplicates.
 */
package lessonq

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/diillson/chatcli/cli/agent/quality"
)

// DeriveJobID returns the 16-hex-char idempotency key for a request.
//
// Inputs:
//   - task: the original user task (normalized: trimmed + lowercased).
//   - trigger: "error" | "hallucination" | "low_quality" | "manual".
//   - attempt: the attempt content (full output, trimmed).
//
// Two triggers with identical normalized inputs produce the same ID —
// caller relies on this for dedupe. Very short inputs still hash
// deterministically; no length minimum.
func DeriveJobID(req quality.LessonRequest) JobID {
	h := sha256.New()
	// Domain separators between fields to prevent accidental
	// collisions (e.g. "a\nb" vs "a" + "\nb").
	h.Write([]byte("task:"))
	h.Write([]byte(normalize(req.Task)))
	h.Write([]byte{0})
	h.Write([]byte("trigger:"))
	h.Write([]byte(strings.ToLower(strings.TrimSpace(req.Trigger))))
	h.Write([]byte{0})
	h.Write([]byte("attempt:"))
	h.Write([]byte(strings.TrimSpace(req.Attempt)))
	h.Write([]byte{0})
	h.Write([]byte("outcome:"))
	h.Write([]byte(strings.TrimSpace(req.Outcome)))

	sum := h.Sum(nil)
	return JobID(hex.EncodeToString(sum[:8]))
}

// normalize lowercases + trims + collapses internal whitespace so
// trivial formatting changes between triggers still collapse to one
// JobID. Used only for the task field where humans write free text.
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return s
	}
	// Collapse runs of whitespace to a single space. Fields() handles
	// tabs/newlines uniformly and is O(n) with no allocation churn.
	parts := strings.Fields(s)
	return strings.Join(parts, " ")
}
