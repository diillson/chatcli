/*
 * ChatCLI - Long-term memory: episodic store.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Episodes are the WHEN of memory: dated, durable units of work ("fixed the
 * OAuth refresh loop in chatcli, shipped as PR #1047") that survive forever,
 * unlike daily notes (30-day TTL) and their lossy rollup digests. They answer
 * the questions facts cannot: "what did we do three months ago?", "what
 * happened in project X in April?". Facts capture what is TRUE; episodes
 * capture what HAPPENED.
 *
 * Persistence follows the same disciplines as the fact index (see persist.go
 * and facts.go): atomic same-directory rename writes, quarantine-never-
 * overwrite on corruption, and reconciling (not last-writer-wins) rewrites so
 * the REPL, gateway daemon and MCP server can all append without erasing each
 * other's history. Episodes are immutable once written, which keeps the merge
 * a plain union by ID.
 */
package memory

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/diillson/chatcli/cli/ctxmgr"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Episode is a single dated unit of work: what was done, where, and how it
// ended. Refs carry the concrete anchors (file paths, PR numbers, commands)
// so a recalled episode is actionable, not just an anecdote.
type Episode struct {
	ID        string    `json:"id"`
	Date      time.Time `json:"date"` // when the work happened
	Project   string    `json:"project,omitempty"`
	Summary   string    `json:"summary"`
	Outcome   string    `json:"outcome,omitempty"`
	Refs      []string  `json:"refs,omitempty"`
	Source    string    `json:"source,omitempty"` // extraction | user
	CreatedAt time.Time `json:"created_at"`
}

// EpisodeStore manages the append-only episodic timeline with JSON
// persistence shared across processes.
type EpisodeStore struct {
	mu       sync.RWMutex
	episodes map[string]*Episode // keyed by ID
	path     string
	logger   *zap.Logger
	maxCount int

	// changeNotifier marks derived caches (knowledge graph) stale on
	// content mutations. See change_notify.go for the non-caller list.
	changeNotifier
}

// NewEpisodeStore creates the store and loads episodes.json from memoryDir.
func NewEpisodeStore(memoryDir string, maxCount int, logger *zap.Logger) *EpisodeStore {
	es := &EpisodeStore{
		episodes: make(map[string]*Episode),
		path:     fmt.Sprintf("%s/episodes.json", memoryDir),
		logger:   logger,
		maxCount: maxCount,
	}
	es.load()
	return es
}

// Add appends an episode. The ID is derived from the calendar day, project
// and normalized summary, so the same work item re-extracted from overlapping
// conversation segments lands on the same episode instead of duplicating.
// Returns true when a new episode was actually stored.
func (es *EpisodeStore) Add(e Episode) bool {
	e.Summary = strings.TrimSpace(e.Summary)
	if e.Summary == "" {
		return false
	}
	if e.Date.IsZero() {
		e.Date = time.Now()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	e.Outcome = strings.TrimSpace(e.Outcome)
	e.ID = episodeID(e.Date, e.Project, e.Summary)

	es.mu.Lock()
	defer es.mu.Unlock()

	if existing, ok := es.episodes[e.ID]; ok {
		// Same work item observed again: enrich in place (an outcome or refs
		// reported by a later segment complete the earlier sighting), never
		// duplicate.
		changed := false
		if existing.Outcome == "" && e.Outcome != "" {
			existing.Outcome = e.Outcome
			changed = true
		}
		if merged := mergeRefs(existing.Refs, e.Refs); len(merged) > len(existing.Refs) {
			existing.Refs = merged
			changed = true
		}
		if changed {
			es.notifyChanged()
			es.persistLocked()
		}
		return false
	}

	es.episodes[e.ID] = &e
	es.notifyChanged()
	es.persistLocked()
	return true
}

// Range returns episodes chronologically (oldest first), filtered by an
// optional [from, to) window, project substring and content query. A zero
// time disables that bound; limit <= 0 means no limit. When limit trims the
// result, the MOST RECENT matches win — the tail of the timeline is almost
// always what the caller is after.
func (es *EpisodeStore) Range(from, to time.Time, project, query string, limit int) []*Episode {
	es.mu.RLock()
	defer es.mu.RUnlock()

	projectLower := strings.ToLower(strings.TrimSpace(project))
	queryTokens := significantTokensOf(strings.ToLower(strings.TrimSpace(query)))

	out := make([]*Episode, 0, len(es.episodes))
	for _, e := range es.episodes {
		if !from.IsZero() && e.Date.Before(from) {
			continue
		}
		if !to.IsZero() && !e.Date.Before(to) {
			continue
		}
		if projectLower != "" && !strings.Contains(strings.ToLower(e.Project), projectLower) {
			continue
		}
		if len(queryTokens) > 0 && !episodeMatchesTokens(e, queryTokens) {
			continue
		}
		out = append(out, e)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// Recent returns the newest episodes within the last d, capped at limit —
// the retriever's "what have we been doing lately" section.
func (es *EpisodeStore) Recent(d time.Duration, limit int) []*Episode {
	return es.Range(time.Now().Add(-d), time.Time{}, "", "", limit)
}

// Count returns the number of stored episodes.
func (es *EpisodeStore) Count() int {
	es.mu.RLock()
	defer es.mu.RUnlock()
	return len(es.episodes)
}

// FormatEpisodes renders episodes as compact dated bullets, oldest first.
// Model-facing English, like the other retrieval sections; human-facing
// callers translate around it.
func FormatEpisodes(eps []*Episode) string {
	if len(eps) == 0 {
		return ""
	}
	var b strings.Builder
	for _, e := range eps {
		b.WriteString("- ")
		b.WriteString(e.Date.Format("2006-01-02"))
		if e.Project != "" {
			b.WriteString(" [" + e.Project + "]")
		}
		b.WriteString(" " + e.Summary)
		if e.Outcome != "" {
			b.WriteString(" — " + e.Outcome)
		}
		if len(e.Refs) > 0 {
			b.WriteString(" (refs: " + strings.Join(e.Refs, ", ") + ")")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// episodeMatchesTokens reports whether every query token appears somewhere in
// the episode's text (summary, outcome, project or refs). AND semantics keep
// multi-word queries precise.
func episodeMatchesTokens(e *Episode, tokens []string) bool {
	hay := strings.ToLower(e.Summary + " " + e.Outcome + " " + e.Project + " " + strings.Join(e.Refs, " "))
	for _, t := range tokens {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// significantTokensOf splits a query into lowercase tokens, dropping
// single-character noise.
func significantTokensOf(q string) []string {
	fields := strings.Fields(q)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) > 1 {
			out = append(out, f)
		}
	}
	return out
}

// episodeID derives the stable identity of a work item: same calendar day +
// project + normalized summary → same episode.
func episodeID(date time.Time, project, summary string) string {
	norm := strings.ToLower(strings.Join(strings.Fields(summary), " "))
	h := sha256.Sum256([]byte(date.Format("2006-01-02") + "|" + project + "|" + norm))
	return fmt.Sprintf("ep_%x", h[:12])
}

// mergeRefs unions b into a preserving order and uniqueness.
func mergeRefs(a, b []string) []string {
	seen := make(map[string]bool, len(a))
	out := make([]string, 0, len(a)+len(b))
	for _, r := range a {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	for _, r := range b {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

func (es *EpisodeStore) load() {
	data, err := os.ReadFile(es.path)
	if err != nil {
		if !os.IsNotExist(err) {
			es.logger.Debug("failed to load episode store", zap.Error(err))
		}
		return
	}

	var episodes []*Episode
	if err := json.Unmarshal(data, &episodes); err != nil {
		// Same discipline as the fact index: quarantine, never leave an
		// unparseable file under the live name where the next persist would
		// silently erase the whole timeline.
		if qpath, qerr := quarantineCorrupt(es.path); qerr == nil {
			es.logger.Warn("episode store corrupt; quarantined for recovery",
				zap.String("quarantine", qpath), zap.Error(err))
		} else {
			es.logger.Warn("episode store corrupt and quarantine failed; refusing to start empty over it",
				zap.Error(qerr))
		}
		return
	}

	for _, e := range episodes {
		if e != nil && e.ID != "" {
			es.episodes[e.ID] = e
		}
	}
	es.logger.Debug("loaded episode store", zap.Int("count", len(es.episodes)))
}

// persistLocked reconciles with the shared on-disk state and rewrites the
// file atomically. Episodes are immutable-by-ID, so reconciliation is a plain
// union: adopt every episode the other process appended, keep ours, and only
// then apply the cap. Caller must hold the write lock.
func (es *EpisodeStore) persistLocked() {
	es.mergeFromDiskLocked()

	episodes := make([]*Episode, 0, len(es.episodes))
	for _, e := range es.episodes {
		episodes = append(episodes, e)
	}
	sort.Slice(episodes, func(i, j int) bool { return episodes[i].Date.Before(episodes[j].Date) })

	data, err := json.MarshalIndent(episodes, "", "  ")
	if err != nil {
		es.logger.Warn("failed to marshal episode store", zap.Error(err))
		return
	}
	if err := atomicWriteFile(es.path, data, 0o600); err != nil {
		es.logger.Warn("failed to write episode store", zap.Error(err))
	}
}

// mergeFromDiskLocked unions episodes persisted by other processes, enriches
// shared IDs with any outcome/refs the other side learned, then drops the
// OLDEST episodes past the cap. Read failures leave the current view
// untouched. Caller must hold the write lock.
func (es *EpisodeStore) mergeFromDiskLocked() {
	data, err := os.ReadFile(es.path)
	if err == nil {
		var onDisk []*Episode
		if json.Unmarshal(data, &onDisk) == nil {
			for _, de := range onDisk {
				if de == nil || de.ID == "" {
					continue
				}
				cur, ok := es.episodes[de.ID]
				if !ok {
					es.episodes[de.ID] = de
					continue
				}
				if cur.Outcome == "" && de.Outcome != "" {
					cur.Outcome = de.Outcome
				}
				cur.Refs = mergeRefs(cur.Refs, de.Refs)
			}
		}
	}

	if es.maxCount > 0 && len(es.episodes) > es.maxCount {
		all := make([]*Episode, 0, len(es.episodes))
		for _, e := range es.episodes {
			all = append(all, e)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Date.Before(all[j].Date) })
		for _, e := range all[:len(all)-es.maxCount] {
			delete(es.episodes, e.ID)
		}
	}
}

// Search ranks episodes by BM25 relevance to query (title, summary,
// project and tags) and returns up to k, best first. Episodes sharing no
// term with the query are absent, so callers that need "the window wins"
// semantics fall back to Range with an empty query. Keyless: the transient
// index over a session-sized store builds in microseconds.
func (es *EpisodeStore) Search(query string, k int) []*Episode {
	q := strings.TrimSpace(query)
	if q == "" || k <= 0 {
		return nil
	}
	es.mu.RLock()
	eps := make([]*Episode, 0, len(es.episodes))
	for _, e := range es.episodes {
		eps = append(eps, e)
	}
	es.mu.RUnlock()
	if len(eps) == 0 {
		return nil
	}
	// Deterministic input order so BM25 ties resolve the same way every call.
	sort.Slice(eps, func(i, j int) bool {
		if !eps[i].Date.Equal(eps[j].Date) {
			return eps[i].Date.Before(eps[j].Date)
		}
		return eps[i].ID < eps[j].ID
	})
	docs := make([]string, len(eps))
	for i, e := range eps {
		docs[i] = episodeSearchText(e)
	}
	hits := ctxmgr.RankDocsBM25(docs, q, k)
	out := make([]*Episode, 0, len(hits))
	for _, h := range hits {
		out = append(out, eps[h.Index])
	}
	return out
}

func episodeSearchText(e *Episode) string {
	var b strings.Builder
	b.WriteString(e.Summary)
	b.WriteByte('\n')
	b.WriteString(e.Outcome)
	b.WriteByte('\n')
	b.WriteString(e.Project)
	for _, r := range e.Refs {
		b.WriteByte(' ')
		b.WriteString(r)
	}
	return b.String()
}

// SearchWithin is Search restricted to a [from, to) window and project
// (zero time / empty project disable that bound): the timeline tool's
// "what did we do about X in April" — ranked by relevance inside the
// window, returned best first.
func (es *EpisodeStore) SearchWithin(from, to time.Time, project, query string, k int) []*Episode {
	q := strings.TrimSpace(query)
	if q == "" || k <= 0 {
		return nil
	}
	window := es.Range(from, to, project, "", 0)
	if len(window) == 0 {
		return nil
	}
	docs := make([]string, len(window))
	for i, e := range window {
		docs[i] = episodeSearchText(e)
	}
	hits := ctxmgr.RankDocsBM25(docs, q, k)
	out := make([]*Episode, 0, len(hits))
	for _, h := range hits {
		out = append(out, window[h.Index])
	}
	return out
}
