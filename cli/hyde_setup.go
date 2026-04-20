/*
 * ChatCLI - HyDE wiring (Phase 3 of seven-pattern rollout).
 *
 * Builds the per-call HyDE augmenter (3a) and ensures the vector
 * index (3b) is attached to the memory store. Both are no-ops when
 * the user hasn't enabled HyDE in /config quality, so the steady
 * state for non-HyDE users is one extra branch and zero allocation.
 */
package cli

import (
	"context"
	"sync"

	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/llm/embedding"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// hydeOnce makes sure the embedding provider is constructed once per
// session — the vector index keeps a stable reference and on-disk
// state stays consistent across calls.
var hydeOnce sync.Once
var hydeProvider embedding.Provider

// hydeProviderForSession returns the embedding provider configured
// via CHATCLI_EMBED_PROVIDER, instantiating it on first use. Returns
// the null provider when nothing is configured or when construction
// fails (logged once).
func (cli *ChatCLI) hydeProviderForSession() embedding.Provider {
	hydeOnce.Do(func() {
		p, err := embedding.NewFromEnv()
		if err != nil {
			cli.logger.Warn("HyDE: embedding provider construction failed; falling back to null",
				zap.Error(err))
			hydeProvider = embedding.NewNull()
			return
		}
		hydeProvider = p
	})
	return hydeProvider
}

// hydeAugmenter constructs a per-call HyDE augmenter wrapping the
// active LLM client. Returns nil when HyDE is disabled in config or
// when no client is wired (early boot, /disconnect window, etc.).
//
// The augmenter uses cli.Client.SendPrompt with a small token budget
// (the hypothesis is at most a few sentences) so the cost surface
// stays tiny.
func (cli *ChatCLI) hydeAugmenter(qcfg quality.Config) *memory.HyDEAugmenter {
	if !qcfg.Enabled || !qcfg.HyDE.Enabled {
		return nil
	}
	if cli.Client == nil {
		return nil
	}
	cb := func(ctx context.Context, prompt string) (string, error) {
		return cli.Client.SendPrompt(ctx, prompt, []models.Message{}, 200)
	}
	return memory.NewHyDEAugmenter(memory.HyDEConfig{
		Enabled:     true,
		NumKeywords: qcfg.HyDE.NumKeywords,
	}, cb, cli.logger)
}

// ensureHyDEVectors ensures a vector index is attached to the memory
// store when HyDE.UseVectors is on AND a real embedding provider is
// configured. Idempotent: subsequent calls reattach the same index.
func (cli *ChatCLI) ensureHyDEVectors(qcfg quality.Config) {
	if !qcfg.Enabled || !qcfg.HyDE.Enabled || !qcfg.HyDE.UseVectors {
		return
	}
	if cli.memoryStore == nil || cli.memoryStore.VectorIndex() != nil {
		return
	}
	provider := cli.hydeProviderForSession()
	if embedding.IsNull(provider) {
		cli.logger.Info("HyDE vectors requested but no embedding provider configured; using keyword-only retrieval",
			zap.String("hint", "set CHATCLI_EMBED_PROVIDER=voyage|openai"))
		return
	}
	memDir := ""
	if mgr := cli.memoryStore.Manager(); mgr != nil {
		memDir = mgr.MemoryDir()
	}
	if memDir == "" {
		cli.logger.Warn("HyDE vectors: memory dir unknown; vectors disabled")
		return
	}
	idx := memory.NewVectorIndex(memDir, provider, cli.logger)
	cli.memoryStore.AttachVectorIndex(idx)
	cli.logger.Info("HyDE vector index attached",
		zap.String("provider", provider.Name()),
		zap.Int("dimension", provider.Dimension()),
		zap.Int("loaded", idx.Count()))
}

// hydeRetrieveContext is the entry point used by chat (cli_llm.go) and
// agent (agent_mode.go) to assemble the workspace context with HyDE
// applied. Falls back to the non-HyDE path when augmenter is nil.
func (cli *ChatCLI) hydeRetrieveContext(ctx context.Context, query string, hints []string, qcfg quality.Config) string {
	if cli.contextBuilder == nil {
		return ""
	}
	cli.ensureHyDEVectors(qcfg)
	aug := cli.hydeAugmenter(qcfg)
	return cli.contextBuilder.BuildSystemPromptPrefixWithHyDE(ctx, query, hints, aug)
}
