/*
 * ChatCLI - Long-term memory: configuration from the environment.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The CHATCLI_MEMORY_* env vars already existed in the config package but were
 * never read on the structured-memory path — NewMemoryStore built the manager
 * with a hard-coded DefaultConfig(), so a deployment that set, say,
 * CHATCLI_MEMORY_RETRIEVAL_BUDGET was silently ignored. This wires the existing
 * knobs through one place. It deliberately introduces NO new environment
 * variables: the blended-ranking tunables stay Config fields with strong
 * defaults, tuned in code rather than multiplying the operator-facing surface.
 */
package memory

import (
	"os"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/config"
)

// ConfigFromEnv starts from DefaultConfig and applies the pre-existing
// CHATCLI_MEMORY_* overrides. Lookups are plain os.Getenv calls — no path or
// shell assumptions — so behavior is identical on Windows, Linux and macOS.
// The result is always sanitized, so a malformed override degrades to the
// default rather than corrupting retrieval.
func ConfigFromEnv() Config {
	c := DefaultConfig()
	if v, ok := envPositiveInt(config.MemoryMaxSizeEnv); ok {
		c.MaxMemoryMDSize = v
	}
	if v, ok := envPositiveInt(config.MemoryRetentionEnv); ok {
		c.DailyNoteRetention = v
	}
	if v, ok := envPositiveInt(config.MemoryMaxFactsEnv); ok {
		c.MaxFactsCount = v
	}
	if v, ok := envPositiveInt(config.MemoryRetrievalEnv); ok {
		c.RetrievalBudget = v
	}
	return c.sanitized()
}

// envPositiveInt reads name as a positive integer. Missing, malformed, or
// non-positive values are ignored so a bad override (e.g. a zero budget that
// would blank memory) can never corrupt the config.
func envPositiveInt(name string) (int, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// sanitized clamps every tunable to a safe range so neither a bad env override
// nor a hand-edited config file can push retrieval into a degenerate state
// (empty budget, zero half-life division, out-of-range cosine floor, …). It is
// idempotent: sanitizing an already-valid Config returns it unchanged.
func (c Config) sanitized() Config {
	if c.MaxMemoryMDSize <= 0 {
		c.MaxMemoryMDSize = 32 * 1024
	}
	if c.DailyNoteRetention <= 0 {
		c.DailyNoteRetention = 30
	}
	if c.MaxFactsCount <= 0 {
		c.MaxFactsCount = 500
	}
	if c.CompactionInterval <= 0 {
		c.CompactionInterval = 24
	}
	if c.RetrievalBudget <= 0 {
		c.RetrievalBudget = 4000
	}
	if c.DecayHalfLifeDays <= 0 {
		c.DecayHalfLifeDays = 30.0
	}
	// Cosine ∈ [-1,1]; a floor outside [0,1) is nonsensical for text
	// embeddings and would either admit anti-correlated junk or reject
	// everything, so fall back to the default.
	if c.MinCosineScore < 0 || c.MinCosineScore >= 1 {
		c.MinCosineScore = 0.25
	}
	if c.VectorTopK <= 0 {
		c.VectorTopK = 12
	}
	if c.BackfillBatchMax <= 0 {
		c.BackfillBatchMax = 500
	}
	c.RankWeights = c.RankWeights.normalized()
	return c
}
