/*
 * ChatCLI - Refine convergence wiring (Phase 5 enterprise).
 *
 * Builds a convergence.Composite cascade (char → jaccard → embedding)
 * from the active embedding provider (shared with HyDE so there's no
 * duplicate provider instance). Wired by AgentMode.initWorkers into
 * quality.BuildPipelineDeps.ConvergenceChecker.
 *
 * Degrades gracefully: when semantic convergence is disabled in
 * config, returns nil and the RefineHook falls back to the legacy
 * char-level heuristic. When the embedding scorer is enabled but the
 * provider is the null one (no CHATCLI_EMBED_PROVIDER configured),
 * the cascade still runs char + jaccard, and embedding-related
 * thresholds are never applied.
 */
package cli

import (
	"time"

	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/cli/agent/quality/convergence"
	"github.com/diillson/chatcli/llm/embedding"
)

// buildRefineConvergence assembles the composite scorer cascade for
// refine stop-detection. Returns nil when the feature is disabled,
// signaling RefineHook to fall back to the legacy char heuristic.
//
// The cascade lifetime is one AgentMode initialization (rebuilt each
// time /agent is entered) so config toggles take effect between runs
// without needing process restart.
func (cli *ChatCLI) buildRefineConvergence(cfg quality.Config) quality.ConvergenceChecker {
	c := cfg.Refine.Convergence
	if !cfg.Refine.Enabled || !c.Enabled {
		return nil
	}

	char := convergence.NewCharScorer()
	jaccard := convergence.NewJaccardScorer()

	var embedScorer *convergence.EmbeddingScorer
	if c.EmbeddingEnabled {
		provider := cli.hydeProviderForSession()
		if !embedding.IsNull(provider) {
			ecfg := convergence.DefaultEmbeddingScorerConfig()
			if c.CacheSize > 0 {
				ecfg.CacheSize = c.CacheSize
			}
			if c.CacheTTLMinutes > 0 {
				ecfg.CacheTTL = time.Duration(c.CacheTTLMinutes) * time.Minute
			}
			if c.BreakerThreshold > 0 {
				ecfg.BreakerThreshold = c.BreakerThreshold
			}
			embedScorer = convergence.NewEmbeddingScorer(provider, ecfg)
		}
	}

	ccfg := convergence.DefaultCompositeConfig()
	if c.CharHighSim > 0 {
		ccfg.CharHighSim = c.CharHighSim
	}
	if c.CharLowSim > 0 {
		ccfg.CharLowSim = c.CharLowSim
	}
	if c.JaccardHighSim > 0 {
		ccfg.JaccardHighSim = c.JaccardHighSim
	}
	if c.EmbeddingConvergedAt > 0 {
		ccfg.EmbeddingConvergedAt = c.EmbeddingConvergedAt
	}
	ccfg.Strict = c.Strict

	return convergence.NewComposite(char, jaccard, embedScorer, ccfg)
}
