/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package quality implements the seven-pattern quality pipeline that wraps
// the worker dispatcher. Patterns: ReAct (already in worker_react), Plan,
// Reflexion, RAG+HyDE, Self-Refine, CoVe and reasoning backbone wiring.
//
// The pipeline is OPT-IN: when no pipeline is wired into the dispatcher,
// behavior is byte-identical to the previous code path.
package quality

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config bundles the knobs for every pattern handled by the pipeline.
//
// All sub-configs are value types (not pointers) so a zero Config is safe
// to use — Defaults() returns the production-ready baseline.
type Config struct {
	Enabled bool // master switch; when false the pipeline is a no-op

	Refine    RefineConfig
	Verify    VerifyConfig
	Reflexion ReflexionConfig
	PlanFirst PlanFirstConfig
	HyDE      HyDEConfig
	Reasoning ReasoningConfig
}

// RefineConfig controls Self-Refine (#5).
type RefineConfig struct {
	Enabled       bool
	MaxPasses     int      // hard cap; default 1
	MinDraftBytes int      // skip refine if draft is shorter than this
	EpsilonChars  int      // char-level fallback threshold (rewrite ≈ draft → stop)
	ExcludeAgents []string // agents whose output is not refined (e.g. formatter)

	// Convergence controls the semantic-convergence cascade that
	// determines when refine stops early. When Convergence.Enabled is
	// true (default) and an embedding provider is wired, refine uses
	// a char → jaccard → embedding cascade to detect semantic
	// stability — catching "same meaning, different words" cases that
	// the char heuristic misses.
	//
	// When Convergence.Enabled is false, the hook falls back to the
	// legacy char-level heuristic (EpsilonChars). Identical behavior
	// to the pre-Convergence implementation.
	Convergence RefineConvergenceConfig
}

// RefineConvergenceConfig tunes the semantic convergence cascade.
// Zero values are ALL fine (defaults from DefaultRefineConvergence).
type RefineConvergenceConfig struct {
	Enabled              bool    // master switch for the cascade
	EmbeddingEnabled     bool    // include the embedding scorer (costs $$)
	Strict               bool    // refuse convergence when embedding unavailable
	CharHighSim          float64 // char short-circuit "identical" threshold
	CharLowSim           float64 // char short-circuit "diverged" threshold
	JaccardHighSim       float64 // jaccard short-circuit threshold
	EmbeddingConvergedAt float64 // final embedding similarity threshold
	CacheSize            int     // embed cache size (LRU)
	CacheTTLMinutes      int     // embed cache TTL in minutes
	BreakerThreshold     int     // consecutive failures before breaker opens
}

// DefaultRefineConvergence returns production-ready convergence
// thresholds.
func DefaultRefineConvergence() RefineConvergenceConfig {
	return RefineConvergenceConfig{
		Enabled:              true,
		EmbeddingEnabled:     false, // default off — requires explicit opt-in due to $ cost
		Strict:               false,
		CharHighSim:          0.99,
		CharLowSim:           0.3,
		JaccardHighSim:       0.95,
		EmbeddingConvergedAt: 0.92,
		CacheSize:            256,
		CacheTTLMinutes:      5,
		BreakerThreshold:     3,
	}
}

// VerifyConfig controls Chain-of-Verification (#6).
type VerifyConfig struct {
	Enabled              bool
	NumQuestions         int      // verification questions per answer; default 3
	RewriteOnDiscrepancy bool     // when true, rewrite the answer addressing discrepancies
	ExcludeAgents        []string // agents whose output is not verified
}

// ReflexionConfig controls Reflexion (#3).
type ReflexionConfig struct {
	Enabled         bool
	OnError         bool // trigger when worker returns Error
	OnHallucination bool // trigger when verifier flags discrepancy
	OnLowQuality    bool // trigger when refiner gives low score
	Persist         bool // persist lessons to memory.Fact

	// Queue governs the durable lesson queue (WAL + worker pool +
	// DLQ). When Queue.Enabled is true, ReflexionHook routes triggers
	// through lessonq.Runner instead of firing a detached goroutine;
	// the runner survives crashes via WAL replay on boot.
	Queue ReflexionQueueConfig
}

// ReflexionQueueConfig controls the durable reflexion queue. When
// disabled, ReflexionHook falls back to the legacy detached-goroutine
// path (non-durable, lessons lost on crash).
type ReflexionQueueConfig struct {
	Enabled             bool
	Workers             int           // worker pool size (default 2)
	Capacity            int           // bounded in-memory depth (default 1000)
	OverflowDropOldest  bool          // true = drop oldest; false = block with timeout
	EnqueueBlockTimeout time.Duration // wait budget when queue is full
	MaxAttempts         int           // retry budget per job (default 5)
	InitialDelay        time.Duration // first retry delay (default 1s)
	MaxDelay            time.Duration // cap on retry back-off (default 5m)
	JitterFraction      float64       // [0..0.5] uniform jitter on back-off
	PerJobTimeout       time.Duration // bound on a single Processor call
	StaleAfter          time.Duration // discard WAL records older than this on replay
	BaseDir             string        // override for WAL/DLQ root; empty = derive from workspace
}

// PlanFirstConfig controls Plan-and-Solve / ReWOO (#2).
type PlanFirstConfig struct {
	Mode                string // "off" | "auto" | "always"
	ComplexityThreshold int    // 0..10; auto triggers when score >= threshold
}

// HyDEConfig controls Hypothetical Document Embeddings retrieval (#4).
//
// Provider selection lives in CHATCLI_EMBED_PROVIDER (read by the
// embedding factory, not this struct) — there is intentionally no
// EmbedProvider field here. An older CHATCLI_QUALITY_HYDE_PROVIDER var
// was removed because it only annotated the display and never wired
// the actual provider, which made it a silent footgun.
type HyDEConfig struct {
	Enabled     bool
	UseVectors  bool // if true, use embedding provider for cosine search
	NumKeywords int  // top-N keywords extracted from hypothesis
}

// ReasoningConfig controls cross-provider reasoning backbone wiring (#7).
type ReasoningConfig struct {
	Mode       string   // "off" | "on" | "auto"
	Budget     int      // thinking budget tokens (Anthropic); maps to "high" on OpenAI
	AutoAgents []string // agents that always get reasoning enabled
}

// Defaults returns the production-ready baseline.
//
// Defaults are conservative: heavy patterns (Refine, Verify, HyDE) start
// disabled to avoid a latency/cost regression for users who only run
// `chatcli` and never touch /config quality. Reflexion stays on because it
// only fires on errors (rare in normal use) and the lessons it produces
// pay back on subsequent runs.
func Defaults() Config {
	return Config{
		Enabled: true,
		Refine: RefineConfig{
			Enabled:       false,
			MaxPasses:     1,
			MinDraftBytes: 200,
			EpsilonChars:  50,
			// refiner/verifier are excluded so the post-hook doesn't
			// loop on its own output (would be infinite recursion
			// otherwise: refine → refine → refine …).
			ExcludeAgents: []string{"formatter", "deps", "refiner", "verifier"},
			Convergence:   DefaultRefineConvergence(),
		},
		Verify: VerifyConfig{
			Enabled:              false,
			NumQuestions:         3,
			RewriteOnDiscrepancy: true,
			ExcludeAgents:        []string{"formatter", "deps", "shell", "refiner", "verifier"},
		},
		Reflexion: ReflexionConfig{
			Enabled:         true,
			OnError:         true,
			OnHallucination: true,
			OnLowQuality:    false,
			Persist:         true,
			Queue: ReflexionQueueConfig{
				Enabled:             true,
				Workers:             2,
				Capacity:            1000,
				OverflowDropOldest:  false,
				EnqueueBlockTimeout: 5 * time.Second,
				MaxAttempts:         5,
				InitialDelay:        time.Second,
				MaxDelay:            5 * time.Minute,
				JitterFraction:      0.2,
				PerJobTimeout:       2 * time.Minute,
				StaleAfter:          7 * 24 * time.Hour,
				BaseDir:             "",
			},
		},
		PlanFirst: PlanFirstConfig{
			Mode:                "auto",
			ComplexityThreshold: 6,
		},
		HyDE: HyDEConfig{
			Enabled:     false,
			UseVectors:  false,
			NumKeywords: 5,
		},
		Reasoning: ReasoningConfig{
			Mode:       "auto",
			Budget:     8000,
			AutoAgents: []string{"planner", "refiner", "verifier", "reflexion"},
		},
	}
}

// LoadFromEnv reads CHATCLI_QUALITY_* overrides on top of Defaults().
//
// All variables are optional. Unset → default applies. Boolean parsing
// accepts: 1/0, true/false, yes/no, on/off (case-insensitive).
func LoadFromEnv() Config {
	cfg := Defaults()
	if v := os.Getenv("CHATCLI_QUALITY_ENABLED"); v != "" {
		cfg.Enabled = parseBool(v, cfg.Enabled)
	}
	loadRefineEnv(&cfg.Refine)
	loadVerifyEnv(&cfg.Verify)
	loadReflexionEnv(&cfg.Reflexion)
	loadPlanFirstEnv(&cfg.PlanFirst)
	loadHyDEEnv(&cfg.HyDE)
	loadReasoningEnv(&cfg.Reasoning)
	return cfg
}

func loadRefineEnv(c *RefineConfig) {
	if v := os.Getenv("CHATCLI_QUALITY_REFINE_ENABLED"); v != "" {
		c.Enabled = parseBool(v, c.Enabled)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFINE_MAX_PASSES"); v != "" {
		c.MaxPasses = parseInt(v, c.MaxPasses)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFINE_MIN_BYTES"); v != "" {
		c.MinDraftBytes = parseInt(v, c.MinDraftBytes)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFINE_EPSILON"); v != "" {
		c.EpsilonChars = parseInt(v, c.EpsilonChars)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFINE_EXCLUDE"); v != "" {
		c.ExcludeAgents = parseList(v)
	}
	loadRefineConvergenceEnv(&c.Convergence)
}

func loadRefineConvergenceEnv(c *RefineConvergenceConfig) {
	if v := os.Getenv("CHATCLI_QUALITY_REFINE_CONVERGENCE_ENABLED"); v != "" {
		c.Enabled = parseBool(v, c.Enabled)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFINE_CONVERGENCE_EMBEDDING"); v != "" {
		c.EmbeddingEnabled = parseBool(v, c.EmbeddingEnabled)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFINE_CONVERGENCE_STRICT"); v != "" {
		c.Strict = parseBool(v, c.Strict)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFINE_CONVERGENCE_CHAR_HIGH"); v != "" {
		c.CharHighSim = parseFloat(v, c.CharHighSim)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFINE_CONVERGENCE_CHAR_LOW"); v != "" {
		c.CharLowSim = parseFloat(v, c.CharLowSim)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFINE_CONVERGENCE_JACCARD_HIGH"); v != "" {
		c.JaccardHighSim = parseFloat(v, c.JaccardHighSim)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFINE_CONVERGENCE_EMBEDDING_SIM"); v != "" {
		c.EmbeddingConvergedAt = parseFloat(v, c.EmbeddingConvergedAt)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFINE_CONVERGENCE_CACHE_SIZE"); v != "" {
		c.CacheSize = parseInt(v, c.CacheSize)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFINE_CONVERGENCE_CACHE_TTL_MIN"); v != "" {
		c.CacheTTLMinutes = parseInt(v, c.CacheTTLMinutes)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFINE_CONVERGENCE_BREAKER_THRESHOLD"); v != "" {
		c.BreakerThreshold = parseInt(v, c.BreakerThreshold)
	}
}

func loadVerifyEnv(c *VerifyConfig) {
	if v := os.Getenv("CHATCLI_QUALITY_VERIFY_ENABLED"); v != "" {
		c.Enabled = parseBool(v, c.Enabled)
	}
	if v := os.Getenv("CHATCLI_QUALITY_VERIFY_NUM_QUESTIONS"); v != "" {
		c.NumQuestions = parseInt(v, c.NumQuestions)
	}
	if v := os.Getenv("CHATCLI_QUALITY_VERIFY_REWRITE"); v != "" {
		c.RewriteOnDiscrepancy = parseBool(v, c.RewriteOnDiscrepancy)
	}
	if v := os.Getenv("CHATCLI_QUALITY_VERIFY_EXCLUDE"); v != "" {
		c.ExcludeAgents = parseList(v)
	}
}

func loadReflexionEnv(c *ReflexionConfig) {
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_ENABLED"); v != "" {
		c.Enabled = parseBool(v, c.Enabled)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_ON_ERROR"); v != "" {
		c.OnError = parseBool(v, c.OnError)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_ON_HALLUCINATION"); v != "" {
		c.OnHallucination = parseBool(v, c.OnHallucination)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_ON_LOW_QUALITY"); v != "" {
		c.OnLowQuality = parseBool(v, c.OnLowQuality)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_PERSIST"); v != "" {
		c.Persist = parseBool(v, c.Persist)
	}
	loadReflexionQueueEnv(&c.Queue)
}

// loadReflexionQueueEnv reads the CHATCLI_QUALITY_REFLEXION_QUEUE_*
// overrides. Every knob is optional — unset falls back to the Defaults.
func loadReflexionQueueEnv(c *ReflexionQueueConfig) {
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_QUEUE_ENABLED"); v != "" {
		c.Enabled = parseBool(v, c.Enabled)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_QUEUE_WORKERS"); v != "" {
		c.Workers = parseInt(v, c.Workers)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_QUEUE_CAPACITY"); v != "" {
		c.Capacity = parseInt(v, c.Capacity)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_QUEUE_DROP_OLDEST"); v != "" {
		c.OverflowDropOldest = parseBool(v, c.OverflowDropOldest)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_QUEUE_BLOCK_TIMEOUT"); v != "" {
		c.EnqueueBlockTimeout = parseDuration(v, c.EnqueueBlockTimeout)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_QUEUE_MAX_ATTEMPTS"); v != "" {
		c.MaxAttempts = parseInt(v, c.MaxAttempts)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_QUEUE_INITIAL_DELAY"); v != "" {
		c.InitialDelay = parseDuration(v, c.InitialDelay)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_QUEUE_MAX_DELAY"); v != "" {
		c.MaxDelay = parseDuration(v, c.MaxDelay)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_QUEUE_JITTER"); v != "" {
		c.JitterFraction = parseFloat(v, c.JitterFraction)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_QUEUE_JOB_TIMEOUT"); v != "" {
		c.PerJobTimeout = parseDuration(v, c.PerJobTimeout)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_QUEUE_STALE_AFTER"); v != "" {
		c.StaleAfter = parseDuration(v, c.StaleAfter)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REFLEXION_QUEUE_BASE_DIR"); v != "" {
		c.BaseDir = strings.TrimSpace(v)
	}
}

func loadPlanFirstEnv(c *PlanFirstConfig) {
	if v := os.Getenv("CHATCLI_QUALITY_PLAN_FIRST_MODE"); v != "" {
		c.Mode = normalizeMode(v, c.Mode, []string{"off", "auto", "always"})
	}
	if v := os.Getenv("CHATCLI_QUALITY_PLAN_FIRST_THRESHOLD"); v != "" {
		c.ComplexityThreshold = parseInt(v, c.ComplexityThreshold)
	}
}

func loadHyDEEnv(c *HyDEConfig) {
	if v := os.Getenv("CHATCLI_QUALITY_HYDE_ENABLED"); v != "" {
		c.Enabled = parseBool(v, c.Enabled)
	}
	if v := os.Getenv("CHATCLI_QUALITY_HYDE_USE_VECTORS"); v != "" {
		c.UseVectors = parseBool(v, c.UseVectors)
	}
	if v := os.Getenv("CHATCLI_QUALITY_HYDE_NUM_KEYWORDS"); v != "" {
		c.NumKeywords = parseInt(v, c.NumKeywords)
	}
}

func loadReasoningEnv(c *ReasoningConfig) {
	if v := os.Getenv("CHATCLI_QUALITY_REASONING_MODE"); v != "" {
		c.Mode = normalizeMode(v, c.Mode, []string{"off", "on", "auto"})
	}
	if v := os.Getenv("CHATCLI_QUALITY_REASONING_BUDGET"); v != "" {
		c.Budget = parseInt(v, c.Budget)
	}
	if v := os.Getenv("CHATCLI_QUALITY_REASONING_AUTO_AGENTS"); v != "" {
		c.AutoAgents = parseList(v)
	}
}

// AppliesToAgent reports whether a hook should run for a given agent type
// given a per-hook exclude list.
func AppliesToAgent(agent string, excludes []string) bool {
	if len(excludes) == 0 {
		return true
	}
	a := strings.ToLower(strings.TrimSpace(agent))
	for _, ex := range excludes {
		if strings.EqualFold(strings.TrimSpace(ex), a) {
			return false
		}
	}
	return true
}

// ─── Internal parsers ─────────────────────────────────────────────────────

func parseBool(v string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "y", "t":
		return true
	case "0", "false", "no", "off", "n", "f":
		return false
	default:
		return fallback
	}
}

func parseInt(v string, fallback int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		return n
	}
	return fallback
}

// parseFloat accepts "0.2", "1", ".5"; unparseable values fall back.
func parseFloat(v string, fallback float64) float64 {
	if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
		return f
	}
	return fallback
}

// parseDuration accepts a Go duration string ("5s", "1m", "2h30m"); on
// parse error returns fallback. An empty string returns fallback so
// callers can pass os.Getenv output without guarding for "".
func parseDuration(v string, fallback time.Duration) time.Duration {
	s := strings.TrimSpace(v)
	if s == "" {
		return fallback
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return fallback
}

// parseList accepts a comma-separated list, lower-cased and trimmed.
func parseList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.ToLower(strings.TrimSpace(p))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// normalizeMode forces the value into one of the allowed enum strings.
// Unknown values fall back to the previous setting.
func normalizeMode(v, fallback string, allowed []string) string {
	s := strings.ToLower(strings.TrimSpace(v))
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	return fallback
}
