/*
 * ChatCLI - Long-term memory: HyDE (Hypothetical Document Embeddings) — Phase 3a.
 *
 * The classic HyDE technique synthesizes a brief "hypothetical answer"
 * for the user's query and uses it as additional retrieval signal. In
 * the embeddings setting that signal becomes a vector; in our keyword
 * setting it becomes additional keywords merged with the user-derived
 * ones, so FactIndex.Search casts a wider, more semantic net.
 *
 * Phase 3b (vector embeddings) plugs into the same Augment call but
 * also writes vectors to a small SQLite-backed store; the keyword
 * path stays as a no-cost fallback.
 *
 * No coupling to llm/client: callers pass an AugmenterFunc closure so
 * memory stays free of provider imports (avoiding an import cycle).
 */
package memory

import (
	"context"
	"strings"

	"go.uber.org/zap"
)

// AugmenterFunc is the small callback shape the HyDE flow needs to ask
// an LLM for a hypothetical answer. Returning ("", nil) is treated as
// "skip augmentation" — the original hints are returned untouched.
type AugmenterFunc func(ctx context.Context, query string) (string, error)

// HyDEConfig controls hypothesis generation and keyword extraction.
type HyDEConfig struct {
	Enabled     bool
	NumKeywords int    // upper bound on keywords pulled from the hypothesis (0 = use default)
	Prompt      string // optional override for the hypothesis system prompt
}

// DefaultHyDEPrompt is the bilingual hypothesis-generation prompt used
// when HyDEConfig.Prompt is empty. Bilingual because chatcli memory
// stores facts in either locale and we want the keyword bag to cover
// both vocabularies.
const DefaultHyDEPrompt = `You are an assistant that drafts a short, plausible answer to a user's question
so the answer can be used to retrieve related notes from long-term memory.

Rules:
- Write 2 to 4 sentences, no more.
- Be concrete: use the technical nouns, file names, library names, or domain terms
  that would likely appear in any matching note.
- If the question mixes English and Portuguese, write the hypothesis in the same
  mix so retrieval matches notes in either language.
- Do not hedge ("I think", "perhaps"). Just produce the most likely answer text.`

// HyDEAugmenter generates a hypothetical answer to a query and merges
// keywords extracted from it with the caller's original hint set.
type HyDEAugmenter struct {
	cfg    HyDEConfig
	llm    AugmenterFunc
	logger *zap.Logger
}

// NewHyDEAugmenter constructs an augmenter. nil logger upgrades to a
// no-op so callers never have to nil-check.
func NewHyDEAugmenter(cfg HyDEConfig, llm AugmenterFunc, logger *zap.Logger) *HyDEAugmenter {
	if logger == nil {
		logger = zap.NewNop()
	}
	if cfg.NumKeywords <= 0 {
		cfg.NumKeywords = 5
	}
	if cfg.Prompt == "" {
		cfg.Prompt = DefaultHyDEPrompt
	}
	return &HyDEAugmenter{cfg: cfg, llm: llm, logger: logger}
}

// Augment returns the union of originalHints and keywords extracted
// from a freshly generated hypothesis. When HyDE is disabled, the LLM
// callback is nil, the query is empty, or the LLM fails, the original
// hints are returned unchanged so retrieval keeps working.
func (h *HyDEAugmenter) Augment(ctx context.Context, query string, originalHints []string) []string {
	if h == nil || !h.cfg.Enabled || h.llm == nil {
		return originalHints
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return originalHints
	}

	// The hypothesis prompt is the user-facing question framed as the
	// caller-supplied system prompt.
	prompt := h.cfg.Prompt + "\n\nQuestion: " + q + "\n\nHypothetical answer:"
	hyp, err := h.llm(ctx, prompt)
	if err != nil {
		h.logger.Warn("HyDE hypothesis call failed; falling back to plain hints",
			zap.Error(err))
		return originalHints
	}
	hyp = strings.TrimSpace(hyp)
	if hyp == "" {
		return originalHints
	}

	hypKeywords := ExtractKeywords([]string{hyp})
	if len(hypKeywords) > h.cfg.NumKeywords {
		hypKeywords = hypKeywords[:h.cfg.NumKeywords]
	}

	return mergeUniqueLowercase(originalHints, hypKeywords)
}

// LastHypothesisSize is unused at runtime — kept as documentation of
// the typical hypothesis budget. Hypothesis bodies are intentionally
// short (2-4 sentences) so the keyword bag stays focused.
const LastHypothesisSize = 600

// mergeUniqueLowercase preserves original ordering and appends only
// keywords that are not already present (case-insensitive). The
// returned slice is a fresh allocation to avoid aliasing surprises.
func mergeUniqueLowercase(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		k := strings.ToLower(strings.TrimSpace(s))
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, s)
	}
	for _, s := range b {
		k := strings.ToLower(strings.TrimSpace(s))
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, s)
	}
	return out
}
