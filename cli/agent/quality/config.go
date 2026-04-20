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
	EpsilonChars  int      // convergence threshold (rewrite ≈ draft → stop)
	ExcludeAgents []string // agents whose output is not refined (e.g. formatter)
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
}

// PlanFirstConfig controls Plan-and-Solve / ReWOO (#2).
type PlanFirstConfig struct {
	Mode                string // "off" | "auto" | "always"
	ComplexityThreshold int    // 0..10; auto triggers when score >= threshold
}

// HyDEConfig controls Hypothetical Document Embeddings retrieval (#4).
type HyDEConfig struct {
	Enabled       bool
	UseVectors    bool   // if true, use embedding provider for cosine search
	EmbedProvider string // "voyage" | "openai" | ""
	NumKeywords   int    // top-N keywords extracted from hypothesis
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
		},
		PlanFirst: PlanFirstConfig{
			Mode:                "auto",
			ComplexityThreshold: 6,
		},
		HyDE: HyDEConfig{
			Enabled:       false,
			UseVectors:    false,
			EmbedProvider: "",
			NumKeywords:   5,
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
	if v := os.Getenv("CHATCLI_QUALITY_HYDE_PROVIDER"); v != "" {
		c.EmbedProvider = strings.ToLower(strings.TrimSpace(v))
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
