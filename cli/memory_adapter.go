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
//
// Queries carrying a time expression ("what did we do 3 months ago", "o que
// fizemos em abril") additionally get the matching slice of the episodic
// timeline prepended — relevance ranking alone cannot answer "when".
func (a *memoryPluginAdapter) Recall(query string) (string, error) {
	if a.cli.memoryStore == nil {
		return "", fmt.Errorf("memory not enabled")
	}

	// @memory recall has no caller deadline of its own, so bound the optional
	// HyDE hypothesis + embedding round-trips here.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	q := strings.TrimSpace(query)

	var timelineSection string
	if q != "" {
		if from, to, cleaned, ok := memory.ParseTemporalRange(q, time.Now()); ok {
			if eps := conversationalTimeline(a.cli.memoryStore.Manager(), from, to, "", cleaned, 15); len(eps) > 0 {
				timelineSection = fmt.Sprintf("## Timeline %s → %s\n\n%s",
					from.Format("2006-01-02"), to.Format("2006-01-02"), memory.FormatEpisodes(eps))
			}
			// The remaining non-temporal words are the content query; a purely
			// temporal question ("what did we do in april?") keeps the original
			// so fact recall still has something to rank by.
			if strings.TrimSpace(cleaned) != "" {
				q = cleaned
			}
		}
	}

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

	if timelineSection != "" {
		if strings.TrimSpace(out) == "" {
			return timelineSection, nil
		}
		return timelineSection + "\n\n" + out, nil
	}
	if strings.TrimSpace(out) == "" {
		return i18n.T("mem.tool.recall.empty"), nil
	}
	return out, nil
}

// conversationalTimeline resolves a timeline query whose window was derived
// from natural language. The leftover words after removing the time
// expression are conversational chatter as often as content ("what did we DO
// three months ago", "o que FIZEMOS há 3 meses"), so they filter through the
// stop-word-aware keyword extractor first — and when even those keywords
// match nothing inside the window, the window wins and its episodes return
// unfiltered: the date was the real signal.
func conversationalTimeline(mgr *memory.Manager, from, to time.Time, project, cleaned string, limit int) []*memory.Episode {
	kw := strings.Join(memory.ExtractKeywords([]string{cleaned}), " ")
	// Relevance-ranked inside the window first (BM25 over summary, outcome,
	// project and refs), then the legacy token filter, then the window.
	eps := mgr.TimelineRanked(from, to, project, kw, limit)
	if len(eps) == 0 {
		eps = mgr.Timeline(from, to, project, kw, limit)
	}
	if len(eps) == 0 && kw != "" && (!from.IsZero() || !to.IsZero()) {
		eps = mgr.Timeline(from, to, project, "", limit)
	}
	return eps
}

// Timeline implements plugins.TimelineAccessor — the chronological view of
// episodic memory behind `@memory timeline`. from/to accept ISO dates
// (2026-04, 2026-04-12) or natural expressions in EN/PT ("3 months ago",
// "abril"); when both are empty, a time expression inside query sets the
// window instead.
func (a *memoryPluginAdapter) Timeline(project, from, to, query string, limit int) (string, error) {
	if a.cli.memoryStore == nil {
		return "", fmt.Errorf("memory not enabled")
	}

	now := time.Now()
	q := strings.TrimSpace(query)
	var fromT, toT time.Time
	windowFromQuery := false
	if start, _, ok := parseTimelineBound(from, now); ok {
		fromT = start
	}
	if _, end, ok := parseTimelineBound(to, now); ok {
		toT = end
	}
	if fromT.IsZero() && toT.IsZero() && q != "" {
		if start, end, cleaned, ok := memory.ParseTemporalRange(q, now); ok {
			fromT, toT, q = start, end, cleaned
			windowFromQuery = true
		}
	}
	if limit <= 0 {
		limit = 30
	}

	mgr := a.cli.memoryStore.Manager()
	var eps []*memory.Episode
	if windowFromQuery {
		eps = conversationalTimeline(mgr, fromT, toT, project, q, limit)
	} else {
		// Explicit from/to/query arguments are the model speaking precisely —
		// honor them strictly.
		eps = mgr.Timeline(fromT, toT, project, q, limit)
	}
	if len(eps) == 0 {
		return i18n.T("mem.tool.timeline.empty"), nil
	}
	header := fmt.Sprintf("## Timeline (%d episodes", len(eps))
	if !fromT.IsZero() || !toT.IsZero() {
		header += fmt.Sprintf(", %s → %s", boundLabel(fromT), boundLabel(toT))
	}
	header += ")"
	return header + "\n\n" + memory.FormatEpisodes(eps), nil
}

// parseTimelineBound resolves a single from/to input into its [start, end)
// day- or month-window. Accepts ISO day, ISO month, and the same natural
// expressions ParseTemporalRange understands.
func parseTimelineBound(s string, now time.Time) (time.Time, time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, time.Time{}, false
	}
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return t, t.AddDate(0, 0, 1), true
	}
	if t, err := time.ParseInLocation("2006-01", s, now.Location()); err == nil {
		return t, t.AddDate(0, 1, 0), true
	}
	if from, to, _, ok := memory.ParseTemporalRange(s, now); ok {
		return from, to, true
	}
	return time.Time{}, time.Time{}, false
}

// boundLabel renders a window bound for the timeline header, tolerating the
// open ("…") side.
func boundLabel(t time.Time) string {
	if t.IsZero() {
		return "…"
	}
	return t.Format("2006-01-02")
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
