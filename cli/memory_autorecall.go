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

// memoryAutoRecallBlock ranks the fact index against the turn's hints and
// renders the top matches as a compact block, or "" when there is nothing
// relevant to say. Injected facts are marked accessed so reinforcement works
// exactly as it does for full-mode retrieval.
func (cli *ChatCLI) memoryAutoRecallBlock(hints []string) string {
	if !memoryAutoRecallEnabled() || cli.memoryStore == nil || len(hints) == 0 {
		return ""
	}
	mgr := cli.memoryStore.Manager()
	if mgr == nil || mgr.Facts == nil {
		return ""
	}

	facts := mgr.Facts.Search(hints)
	if len(facts) == 0 {
		return ""
	}
	if len(facts) > autoRecallMaxFacts {
		facts = facts[:autoRecallMaxFacts]
	}

	var b strings.Builder
	b.WriteString(autoRecallHeader)
	accessed := make([]string, 0, len(facts))
	for _, f := range facts {
		line := "- [" + f.Category + "] " + f.Content
		if b.Len()+len(line)+1 > autoRecallBudget {
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
		accessed = append(accessed, f.ID)
	}
	if len(accessed) == 0 {
		return ""
	}
	mgr.Facts.MarkAccessed(accessed)

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
