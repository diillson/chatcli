/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ChatCLI - memory_adapter.go
 *
 * Implements plugins.MemoryAdapter so the @memory builtin tool can route
 * ReAct calls into the live memory store. Supplied to
 * plugins.SetMemoryAdapter when the memory store is initialized.
 *
 * Writes go through the deterministic store methods (RememberFact /
 * UpdateProfile / ForgetFacts) — no LLM, no throttling — and invalidate the
 * context builder cache so the next prompt reflects the change.
 */
package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/i18n"
)

// memoryPluginAdapter is the concrete plugins.MemoryAdapter.
type memoryPluginAdapter struct {
	cli *ChatCLI
}

// invalidate refreshes the context builder so freshly written memory is
// visible on the next turn. No-op when the builder is absent.
func (a *memoryPluginAdapter) invalidate() {
	if a.cli.contextBuilder != nil {
		a.cli.contextBuilder.InvalidateCache()
	}
}

func (a *memoryPluginAdapter) Remember(content, category string) (string, error) {
	if a.cli.memoryStore == nil {
		return "", fmt.Errorf("memory not enabled")
	}
	added := a.cli.memoryStore.RememberFact(content, category)
	a.invalidate()
	if !added {
		return i18n.T("mem.tool.remember.dup"), nil
	}
	return i18n.T("mem.tool.remember.ok", content), nil
}

func (a *memoryPluginAdapter) UpdateProfile(updates map[string]string) (string, error) {
	if a.cli.memoryStore == nil {
		return "", fmt.Errorf("memory not enabled")
	}
	changed := a.cli.memoryStore.UpdateProfile(updates)
	a.invalidate()
	if !changed {
		return i18n.T("mem.tool.profile.nochange"), nil
	}
	keys := make([]string, 0, len(updates))
	for k := range updates {
		keys = append(keys, k)
	}
	return i18n.T("mem.tool.profile.ok", strings.Join(keys, ", ")), nil
}

func (a *memoryPluginAdapter) Forget(match string) (string, error) {
	if a.cli.memoryStore == nil {
		return "", fmt.Errorf("memory not enabled")
	}
	n := a.cli.memoryStore.ForgetFacts(match)
	a.invalidate()
	return i18n.T("mem.tool.forget.result", n), nil
}

// Recall is the on-demand (pull) retrieval primitive behind `@memory recall`.
// It mirrors the quality of the per-turn push retrieval rather than the old
// naive whitespace split: keywords come from ExtractKeywords (stop-word
// filtered), and when HyDE is enabled in /config quality the query is widened
// through the same hypothesis + vector-cosine stack the system-prompt path
// uses. An empty query falls back to the broad memory context.
func (a *memoryPluginAdapter) Recall(query string) (string, error) {
	if a.cli.memoryStore == nil {
		return "", fmt.Errorf("memory not enabled")
	}

	// @memory recall has no caller deadline of its own, so bound the optional
	// HyDE hypothesis + embedding round-trips here.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	q := strings.TrimSpace(query)
	var out string
	switch {
	case q == "":
		out = a.cli.memoryStore.GetMemoryContext()
	default:
		hints := memory.ExtractKeywords([]string{q})
		if len(hints) == 0 {
			// Very short / all-stop-word queries yield no keywords; fall
			// back to the raw fields so recall still narrows by something.
			hints = strings.Fields(q)
		}
		if qcfg := quality.LoadFromEnv(); qcfg.Enabled && qcfg.HyDE.Enabled {
			out = a.cli.memoryStore.GetRelevantContextWithHyDE(ctx, q, hints, a.cli.hydeAugmenterFor(qcfg))
		} else {
			out = a.cli.memoryStore.GetRelevantContext(hints)
		}
	}

	if strings.TrimSpace(out) == "" {
		return i18n.T("mem.tool.recall.empty"), nil
	}
	return out, nil
}

// GraphMap and GraphNeighbors expose the knowledge graph as a relational VIEW of
// the same memory/skill data — the "what connects to this" angle that content
// recall cannot give. The graph is derived on demand; see knowledge_graph.go.
func (a *memoryPluginAdapter) GraphMap() (string, error) {
	return a.cli.graphMapText()
}

func (a *memoryPluginAdapter) GraphNeighbors(query string) (string, error) {
	return a.cli.graphNeighborsText(query)
}
