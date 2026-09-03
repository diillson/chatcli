/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ChatCLI - memory_autorecall.go
 *
 * Proactive recall for the "index" memory mode. The pull model keeps per-turn
 * cost bounded, but it relies on the model CHOOSING to call @memory recall —
 * and models routinely skip the call, answering with a 600-char digest while
 * the relevant gotcha sits unread in the fact index. Auto-recall closes that
 * gap: each chat/agent/coder turn running in index mode, the hints already
 * extracted from recent messages rank the fact index, and the top few matches
 * (tiny, budget-capped) ride into the prompt alongside the digest.
 *
 * Cache discipline: the block is hint-driven, so it changes turn to turn. It
 * is therefore injected into the UNCACHED trailing block (with the wall-clock
 * dynamic context), never into the stable workspace block — a volatile line
 * placed early would poison every cached block after it (see the chat
 * pipeline's stable-prefix/volatile-suffix contract).
 *
 * Gated by CHATCLI_MEMORY_AUTORECALL (default on; only meaningful in "index"
 * mode — "full" already injects the whole retrieval, "off" injects nothing).
 */
package cli

import (
	"context"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"os"
	"strings"

	"github.com/diillson/chatcli/pkg/knowledge"
)

const (
	// autoRecallMaxFacts caps how many facts ride per turn — this is a nudge,
	// not a retrieval; the model pulls detail via @memory recall.
	autoRecallMaxFacts = 3

	// autoRecallBudget caps the fact lines in bytes.
	autoRecallBudget = 700

	// autoRecallRelatedBudget is the extra allowance for the one-line graph
	// expansion appended after the facts. Separate from autoRecallBudget so
	// the graph line never displaces a fact.
	autoRecallRelatedBudget = 220

	// autoRecallRelatedMax caps how many graph neighbors the line names.
	autoRecallRelatedMax = 3

	// autoRecallMinLexical is the keyword-relevance floor for a fact found
	// by keywords alone (computeRelevance scale): roughly a third of the
	// turn's hints must hit the fact. Semantic hits are gated by the cosine
	// floor instead.
	autoRecallMinLexical = 0.34

	// autoRecallMinQueryChars skips the embedding call for queries too
	// short to carry meaning ("ok", "sim").
	autoRecallMinQueryChars = 8
	// autoRecallMaxEpisodes / autoRecallEpisodeBudget bound the timeline
	// hits appended after the facts (BM25 over episodes).
	autoRecallMaxEpisodes    = 2
	autoRecallEpisodeBudget  = 360
	autoRecallEpisodeLineMax = 160
)

// autoRecallHeader is an English model-facing constant, like memoryRecallHint.
const autoRecallHeader = "[MEMORY AUTO-RECALL]\n" +
	"Long-term facts matching the current task (pull full detail or more with @memory recall):\n"

// memoryAutoRecallEnabled reads CHATCLI_MEMORY_AUTORECALL; unset means enabled.
func memoryAutoRecallEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_MEMORY_AUTORECALL"))) {
	case "false", "0", "off", "no", "disabled":
		return false
	}
	return true
}

// memoryAutoRecallBlock is the context-free form kept for callers that have
// no query text; it ranks by keywords only (see memoryAutoRecallBlockCtx).
func (cli *ChatCLI) memoryAutoRecallBlock(hints []string) string {
	return cli.memoryAutoRecallBlockCtx(context.Background(), hints, "")
}

// memoryAutoRecallBlockCtx ranks the fact index against the turn's hints —
// and, when a vector index is wired, against the query's embedding — and
// renders the top matches as a compact block, or "" when nothing clears the
// relevance floor. Two changes from the original lexical-only nudge:
//
//   - the blended ranker (semantic + lexical + temporal) is used whenever
//     vectors exist, so a paraphrase with zero keyword overlap can surface;
//     keyless setups keep the lexical path unchanged;
//   - a lexical floor (autoRecallMinLexical) and the cosine floor
//     (MinCosineScore) gate candidates, so one incidental token no longer
//     injects an unrelated fact into every turn.
//
// Injected facts are NOT marked accessed: reinforcing a fact merely for
// being pushed was self-entrenching (a spuriously surfaced fact climbed its
// own ranking). Access is reinforced when the model actually pulls detail
// through the memory tool, as the pull path already does.
// autoRecallEpisodeLine renders one timeline hit as a dated bullet.
func autoRecallEpisodeLine(e *memory.Episode) string {
	text := strings.TrimSpace(e.Summary)
	if e.Outcome != "" {
		text += " → " + strings.TrimSpace(e.Outcome)
	}
	if len(text) > autoRecallEpisodeLineMax {
		text = text[:autoRecallEpisodeLineMax] + "…"
	}
	return "- [episode " + e.Date.Format("2006-01-02") + "] " + text
}

func (cli *ChatCLI) memoryAutoRecallBlockCtx(ctx context.Context, hints []string, query string) string {
	if !memoryAutoRecallEnabled() || cli.memoryStore == nil || len(hints) == 0 {
		return ""
	}
	mgr := cli.memoryStore.Manager()
	if mgr == nil || mgr.Facts == nil {
		return ""
	}

	cfg := mgr.GetConfig()
	var semantic map[string]float64
	if vectors := mgr.VectorIndex(); vectors != nil && vectors.Enabled() && len(strings.TrimSpace(query)) >= autoRecallMinQueryChars {
		if vec, err := vectors.EmbedQuery(ctx, query); err == nil {
			topK := cfg.VectorTopK
			if topK <= 0 {
				topK = 12
			}
			hits := vectors.SimilarFactsScored(vec, topK, cfg.MinCosineScore)
			if len(hits) > 0 {
				semantic = make(map[string]float64, len(hits))
				for _, h := range hits {
					semantic[h.ID] = h.Score
				}
			}
		}
	}
	facts := mgr.Facts.SearchBlendedMin(hints, semantic, cfg.RankWeights, autoRecallMinLexical)
	if len(facts) == 0 {
		return ""
	}
	if len(facts) > autoRecallMaxFacts {
		facts = facts[:autoRecallMaxFacts]
	}

	var b strings.Builder
	b.WriteString(autoRecallHeader)
	accessed := make([]string, 0, len(facts))
	shown := make([]*memory.Fact, 0, len(facts))
	for _, f := range facts {
		line := "- [" + f.Category + "] " + f.Content
		if b.Len()+len(line)+1 > autoRecallBudget {
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
		accessed = append(accessed, f.ID)
		shown = append(shown, f)
	}
	if len(accessed) == 0 {
		return ""
	}
	// Reinforcement waits for evidence: the reply must draw on a fact
	// before its access counter moves (memory_recall_evidence.go).
	cli.noteRecalledFacts(shown)

	// Episodes: the dated work units the query evidently refers to, ranked
	// by BM25 over the timeline. Bounded to a couple of lines so the block
	// stays a nudge; the timeline tool has the full view.
	if len(strings.TrimSpace(query)) >= autoRecallMinQueryChars {
		for _, e := range mgr.Episodes.Search(query, autoRecallMaxEpisodes) {
			line := autoRecallEpisodeLine(e)
			if b.Len()+len(line)+1 > autoRecallBudget+autoRecallEpisodeBudget {
				break
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}

	// Graph expansion: one compact line naming what is structurally adjacent
	// to the facts above, so the model knows there is MORE to pull before it
	// claims ignorance. Cache-only on purpose — Manager().KnowledgeGraph()
	// is nil when the persisted graph is off (CHATCLI_MEMORY_GRAPH), and this
	// nudge must never pay a derive-per-call rebuild.
	if line := autoRecallRelatedLine(mgr.KnowledgeGraph(), accessed); line != "" &&
		len(line)+1 <= autoRecallRelatedBudget {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// autoRecallRelatedLine renders "Related (graph): ..." from the 1-hop
// neighborhood of the shown facts. Tag and profile nodes are filtered (glue
// and ever-present hub), already-shown facts are skipped, and the pointer
// uses only surfaces every mode has (@memory neighbors) — never @session.
// Returns "" when there is nothing new to point at.
func autoRecallRelatedLine(g *knowledge.Graph, shownFactIDs []string) string {
	if g == nil || g.Len() == 0 {
		return ""
	}
	shown := make(map[string]bool, len(shownFactIDs))
	for _, id := range shownFactIDs {
		shown["fact:"+id] = true
	}
	var names []string
	seen := make(map[string]bool)
	for _, id := range shownFactIDs {
		for _, n := range g.Neighborhood("fact:"+id, 1, autoRecallRelatedMax*2) {
			if len(names) >= autoRecallRelatedMax {
				break
			}
			if seen[n.ID] || shown[n.ID] ||
				n.Kind == knowledge.KindTag || n.Kind == knowledge.KindProfile {
				continue
			}
			seen[n.ID] = true
			title := strings.TrimSpace(n.Title)
			if title == "" {
				title = n.ID
			}
			names = append(names, title+" ("+string(n.Kind)+")")
		}
		if len(names) >= autoRecallRelatedMax {
			break
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "Related (graph): " + strings.Join(names, ", ") +
		" — expand with @memory neighbors <title>."
}
