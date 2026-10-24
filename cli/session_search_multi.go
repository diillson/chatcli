/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * session_search_multi.go — one session search, several phrasings.
 *
 * Saved conversations hold what the user said in THEIR words, which are
 * rarely the words of a later question: "how many projects have I led?" is
 * answered by "I led the data analysis team" in one session and "I presented
 * a poster on my research" in another. Lexical search finds a session only
 * when the query shares its vocabulary, so recall used to depend on the
 * model issuing one search per rephrasing — each one a full agent turn
 * carrying the whole conversation.
 *
 * SearchMany runs every phrasing over the same corpus in one call and fuses
 * the rankings (reciprocal rank fusion): a session ranks by how high it
 * places under each phrasing, a session that fully matches ANY phrasing is a
 * full match, and every hit says which phrasings found it. Each phrasing is
 * ranked exactly as a single search ranks it, so a one-phrasing call is the
 * single search, unchanged.
 */
package cli

import (
	"sort"
	"strings"
	"time"
)

const (
	// maxSearchPhrasings bounds the phrasings one search runs.
	maxSearchPhrasings = 6
	// rrfK is the reciprocal-rank-fusion damping constant (the standard 60).
	rrfK = 60.0
)

// normalizePhrasings trims, de-duplicates (case- and accent-insensitively)
// and bounds the phrasings of one search, keeping their order.
func normalizePhrasings(queries []string) []string {
	out := make([]string, 0, len(queries))
	seen := make(map[string]bool, len(queries))
	for _, q := range queries {
		q = strings.TrimSpace(q)
		key := normalizeForSearch(q)
		if q == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, q)
		if len(out) >= maxSearchPhrasings {
			break
		}
	}
	return out
}

// fuseSessionHits merges the per-phrasing hit lists into one ranking.
// results[i] are the hits of phrasings[i], best first. A session's fused
// score is the sum of 1/(rrfK+rank) over the phrasings that found it; full
// matches (under any phrasing) precede partial-only ones; ties break by the
// best per-phrasing recency-boosted score, then name. Snippets are merged
// round-robin across phrasings (each phrasing's best snippet first), bounded
// by maxSnippets, without repeats.
func fuseSessionHits(phrasings []string, results [][]SessionSearchHit, maxSnippets int, now time.Time) []SessionSearchHit {
	type acc struct {
		hit      SessionSearchHit
		rrf      float64
		best     float64
		full     bool
		snippets [][]string // per phrasing, in phrasing order
	}
	by := make(map[string]*acc)
	for qi, hits := range results {
		for rank, h := range hits {
			a, ok := by[h.Session]
			if !ok {
				a = &acc{hit: SessionSearchHit{Session: h.Session, SavedAt: h.SavedAt, Title: h.Title}}
				a.snippets = make([][]string, len(phrasings))
				by[h.Session] = a
			}
			a.rrf += 1 / (rrfK + float64(rank+1))
			if s := h.Score * sessionRecencyBoostAt(h.SavedAt, now); s > a.best {
				a.best = s
			}
			a.hit.Matches += h.Matches
			a.hit.Score += h.Score
			if !h.Partial {
				a.full = true
			} else if !a.full && h.Covered > a.hit.Covered {
				a.hit.Covered, a.hit.Terms = h.Covered, h.Terms
			}
			a.hit.Via = append(a.hit.Via, phrasings[qi])
			a.snippets[qi] = h.Snippets
		}
	}
	out := make([]SessionSearchHit, 0, len(by))
	ranked := make([]*acc, 0, len(by))
	for _, a := range by {
		ranked = append(ranked, a)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].full != ranked[j].full {
			return ranked[i].full
		}
		if ranked[i].rrf != ranked[j].rrf {
			return ranked[i].rrf > ranked[j].rrf
		}
		if ranked[i].best != ranked[j].best {
			return ranked[i].best > ranked[j].best
		}
		return ranked[i].hit.Session < ranked[j].hit.Session
	})
	for _, a := range ranked {
		h := a.hit
		h.Partial = !a.full
		if a.full {
			h.Covered, h.Terms = 0, 0
		}
		h.Snippets = mergeSnippetsRoundRobin(a.snippets, maxSnippets)
		out = append(out, h)
	}
	return out
}

// mergeSnippetsRoundRobin interleaves per-phrasing snippet lists (first of
// each, then second of each…), dropping repeats, up to max.
func mergeSnippetsRoundRobin(lists [][]string, limit int) []string {
	if limit <= 0 {
		limit = 3
	}
	var out []string
	seen := make(map[string]bool)
	for depth := 0; len(out) < limit; depth++ {
		progressed := false
		for _, l := range lists {
			if depth >= len(l) {
				continue
			}
			progressed = true
			if s := l[depth]; !seen[s] {
				seen[s] = true
				out = append(out, s)
				if len(out) >= limit {
					break
				}
			}
		}
		if !progressed {
			break
		}
	}
	return out
}
