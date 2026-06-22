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

// hydeMu guards the session-cached embedding provider. The provider is
// built lazily on first use — the vector index keeps a stable reference
// and on-disk state stays consistent across calls — and rebuilt by
// refreshEmbeddingProvider when /config reload re-reads the environment.
// A plain sync.Once would latch a Null provider for the whole process
// when CHATCLI_EMBED_PROVIDER only lands in .env after boot.
var hydeMu sync.Mutex
var hydeProvider embedding.Provider
var hydeProviderReady bool

// hydeProviderForSession returns the embedding provider configured
// via CHATCLI_EMBED_PROVIDER, instantiating it on first use. Returns
// the null provider when nothing is configured or when construction
// fails (logged once per build).
func (cli *ChatCLI) hydeProviderForSession() embedding.Provider {
	hydeMu.Lock()
	defer hydeMu.Unlock()
	if !hydeProviderReady {
		hydeProvider = cli.buildEmbeddingProviderLocked()
		hydeProviderReady = true
	}
	return hydeProvider
}

// buildEmbeddingProviderLocked constructs a provider from the current
// environment. Callers must hold hydeMu.
func (cli *ChatCLI) buildEmbeddingProviderLocked() embedding.Provider {
	p, err := embedding.NewFromEnv()
	if err != nil {
		cli.logger.Warn("HyDE: embedding provider construction failed; falling back to null",
			zap.Error(err))
		return embedding.NewNull()
	}
	return p
}

// refreshEmbeddingProvider rebuilds the embedding provider from the
// (re-loaded) environment and rewires every consumer that captured the
// previous instance: /context semantic retrieval and the memory vector
// index. Called by /config reload so a CHATCLI_EMBED_PROVIDER change
// takes effect without restarting the process. Returns the previous and
// current provider names so the caller can surface the transition.
func (cli *ChatCLI) refreshEmbeddingProvider() (oldName, newName string) {
	hydeMu.Lock()
	old := hydeProvider
	fresh := cli.buildEmbeddingProviderLocked()
	hydeProvider = fresh
	hydeProviderReady = true
	hydeMu.Unlock()

	if cli.contextHandler != nil {
		if mgr := cli.contextHandler.GetManager(); mgr != nil {
			mgr.AttachEmbeddingProvider(fresh)
		}
	}

	// Swap the memory vector index only when one was already attached: a
	// fresh index for the new provider (vindex auto-migrates on provider
	// or dimension change), or a detach when embeddings were turned off.
	// When none was attached, ensureHyDEVectors lazily attaches on the
	// next turn and picks up the new provider by itself.
	if cli.memoryStore != nil && cli.memoryStore.VectorIndex() != nil {
		if embedding.IsNull(fresh) {
			cli.memoryStore.AttachVectorIndex(nil)
		} else if mgr := cli.memoryStore.Manager(); mgr != nil && mgr.MemoryDir() != "" {
			cli.memoryStore.AttachVectorIndex(memory.NewVectorIndex(mgr.MemoryDir(), fresh, cli.logger))
		}
	}
	return embeddingProviderLabel(old), embeddingProviderLabel(fresh)
}

// embeddingProviderLabel renders a provider name for status output;
// nil/Null collapse to "null" so transitions compare cleanly.
func embeddingProviderLabel(p embedding.Provider) string {
	if embedding.IsNull(p) {
		return "null"
	}
	return p.Name()
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
			zap.String("hint", "set CHATCLI_EMBED_PROVIDER=voyage|openai|bedrock"))
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

// hydeAugmenterFor resolves the per-call HyDE augmenter: it lazily attaches the
// vector index when needed and returns the augmenter, or nil when HyDE is
// disabled (or no LLM client is wired). This is the single setup seam every
// retrieval path shares — chat, agent/coder, and the @memory recall tool.
//
// Only this preparation step is centralized; what each caller does with the
// augmenter (mode-aware workspace context, direct memory recall, ...)
// legitimately differs and stays at the call site. Downstream builders already
// treat a nil augmenter as the non-HyDE path, so callers can pass the result
// straight through.
func (cli *ChatCLI) hydeAugmenterFor(qcfg quality.Config) *memory.HyDEAugmenter {
	if !qcfg.Enabled || !qcfg.HyDE.Enabled {
		return nil
	}
	cli.ensureHyDEVectors(qcfg)
	return cli.hydeAugmenter(qcfg)
}
