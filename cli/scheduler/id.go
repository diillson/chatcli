/*
 * ChatCLI - Scheduler: JobID derivation.
 *
 * JobIDs are derived deterministically from (name | owner | nonce) via
 * sha256 truncated to 16 hex chars. Deterministic derivation gives us
 * two things for free:
 *
 *   1. Idempotency — re-submitting the same (name, owner) with the
 *      same nonce produces the same ID. WAL Append dedupes on ID so
 *      a nervous caller can retry without creating duplicates.
 *
 *   2. Readability — 16 hex chars is enough entropy to avoid collisions
 *      in a single workspace, and operators can eyeball log lines.
 *
 * For recurring jobs the nonce is the schedule's CreatedAt timestamp
 * (rounded to microseconds); each occurrence gets its own JobID but
 * the *definition* is stable — cancel_by_name uses that stability.
 */
package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// DeriveJobID produces a deterministic ID from the given components.
// nonce may be any string that disambiguates re-submissions (typically
// the created-at timestamp as RFC3339Nano). If nonce is empty a UUID is
// used to guarantee uniqueness.
func DeriveJobID(name string, owner Owner, nonce string) JobID {
	if strings.TrimSpace(nonce) == "" {
		nonce = uuid.New().String()
	}
	h := sha256.New()
	// sha256.Hash.Write never returns a non-nil error, so the errcheck
	// warning would be pure noise — suppress explicitly.
	_, _ = fmt.Fprintf(h, "scheduler-v%d|%s|%s|%s|%s",
		SchemaVersion,
		strings.TrimSpace(name),
		owner.Kind,
		owner.ID,
		nonce,
	)
	sum := h.Sum(nil)
	return JobID(hex.EncodeToString(sum[:8]))
}

// NewJobID returns a random, high-entropy ID. Used when determinism is
// undesirable (e.g. spawning a child job from a Triggers edge — each
// child gets a fresh ID even if the parent reruns).
func NewJobID() JobID {
	u := uuid.New()
	return JobID(strings.ReplaceAll(u.String(), "-", "")[:16])
}
