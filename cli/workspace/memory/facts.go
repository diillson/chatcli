package memory

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"go.uber.org/zap"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// FactIndex manages scored long-term memory facts with JSON persistence.
//
// Multiple ChatCLI processes (REPL and gateway daemon) share the same memory
// directory, so persistence is reconciling, not last-writer-wins: every
// persist first merges the on-disk state (facts learned by the other process)
// and applies tombstones (facts explicitly forgotten by either process)
// before rewriting the file. See mergeFromDiskLocked.
type FactIndex struct {
	latch storeLatch // read-only once a sealed file could not be opened
	// rev counts mutations; dfCache is the token document-frequency table
	// for that revision (IDF weights in computeRelevance).
	rev, dfRev, dfCount int
	dfCache             map[string]int
	dfMu                sync.Mutex
	facts               map[string]*Fact // keyed by ID
	mu                  sync.RWMutex
	path                string // path to memory_index.json
	logger              *zap.Logger
	config              Config

	// tombstones records explicit deletions (id → when) so reconciliation
	// never resurrects a forgotten fact from the shared file — and so the
	// deletion propagates to the other process's next persist. A tombstone
	// kills any copy whose CreatedAt predates it: passive access does not
	// undo a forget, only a deliberate re-add (fresh CreatedAt) does.
	tombstones    map[string]time.Time
	tombstonePath string

	// MarkAccessed debounce: access bumps happen on every retrieval, and a
	// full-file rewrite per retrieval is wasted IO that also widens the
	// multi-process race window. Bumps are folded into the next real persist,
	// or flushed when the interval elapses.
	accessFlushInterval time.Duration
	lastAccessFlush     time.Time
	accessDirty         bool

	// removalHooks are invoked for every explicit fact removal (forget,
	// archive, replace/compaction, supersede, cap prune) so derived
	// per-fact state — the vector index, the knowledge-graph cache — is
	// dropped in lockstep and never orphans. Keyed so independent consumers
	// can attach and detach without clobbering each other (the vector index
	// re-attaches on /config reload and must not silently drop the graph
	// hook). Invoked synchronously with fi.mu held, in sorted key order for
	// determinism; callbacks must not call back into the FactIndex.
	removalHooks map[string]func(ids []string)

	// changeNotifier fires after content mutations (add, reinforce,
	// supersede, compaction replace) so derived caches can mark themselves
	// stale. Deliberately NOT fired by access-metadata bumps (MarkAccessed)
	// or bare persists — those happen every retrieval and would thrash the
	// cache.
	changeNotifier
}

// factAccessFlushInterval bounds how often bare access-metadata bumps rewrite
// the index file. Real mutations always persist immediately.
const factAccessFlushInterval = 30 * time.Second

// tombstoneRetention is how long a deletion marker is kept. Long enough for
// every co-running process to observe it; bounded so the sidecar never grows
// without limit.
const tombstoneRetention = 30 * 24 * time.Hour

// NewFactIndex creates a new fact index.
func NewFactIndex(memoryDir string, config Config, logger *zap.Logger) *FactIndex {
	fi := &FactIndex{
		facts:               make(map[string]*Fact),
		path:                fmt.Sprintf("%s/memory_index.json", memoryDir),
		logger:              logger,
		config:              config,
		tombstones:          make(map[string]time.Time),
		tombstonePath:       fmt.Sprintf("%s/memory_tombstones.json", memoryDir),
		accessFlushInterval: factAccessFlushInterval,
	}
	fi.load()
	return fi
}

// AddFact adds a new fact, deduplicating by content hash.
// Returns true if the fact was actually added (not a duplicate).
func (fi *FactIndex) AddFact(content, category string, tags []string) bool {
	return fi.AddFactWithSource(content, category, tags, "")
}

// Confidence and provenance defaults. Deterministic, user-stated facts are
// trusted more than background-extracted guesses; a fact re-observed over time
// climbs toward certainty. Zero confidence means "legacy/unset" and is treated
// as the extraction default — see Fact.confidence().
const (
	ConfidenceUser       = 0.9 // user/agent stated it deterministically
	ConfidenceCorrection = 1.0 // user explicitly corrected a prior belief
	ConfidenceExtraction = 0.6 // inferred by the background extraction pass
	defaultConfidence    = 0.6

	ProvenanceUser       = "user"
	ProvenanceExtraction = "extraction"
	ProvenanceCorrection = "correction"
	ProvenanceLegacy     = "legacy" // pre-confidence fact enriched at load

	// Reconciliation thresholds over significant-token Jaccard similarity.
	reconcileDuplicateJaccard = 0.85 // ≥ → a rephrasing of an existing fact: reinforce, don't duplicate
	reconcileSupersedeJaccard = 0.5  // [this, dup) + same subject → an update/contradiction: supersede
)

// confidence returns the fact's trust weight, defaulting legacy/unset facts.
func (f *Fact) confidence() float64 {
	if f.Confidence > 0 {
		return f.Confidence
	}
	return defaultConfidence
}

// AddFactWithSource adds a new fact with source project annotation.
// sourceProject is the workspace directory where the fact was learned (may be empty).
func (fi *FactIndex) AddFactWithSource(content, category string, tags []string, sourceProject string) bool {
	return fi.AddFactWithMeta(content, category, tags, sourceProject, ConfidenceExtraction, ProvenanceExtraction)
}

// AddFactWithMeta is AddFactWithSource with explicit confidence and provenance.
// It deduplicates by content hash (reinforcing on an exact repeat) and, before
// inserting, reconciles against same-category facts: a near-duplicate rephrasing
// reinforces the existing fact, and a same-subject update of equal-or-higher
// confidence supersedes the stale one instead of piling up a contradiction.
func (fi *FactIndex) AddFactWithMeta(content, category string, tags []string, sourceProject string, confidence float64, provenance string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	if confidence <= 0 {
		confidence = defaultConfidence
	}

	id := fi.hashContent(content)

	fi.mu.Lock()
	defer fi.mu.Unlock()

	// Exact duplicate (same content hash) — reinforce.
	if existing, ok := fi.facts[id]; ok {
		fi.reinforceLocked(existing, sourceProject, confidence, provenance)
		fi.notifyChanged()
		fi.persistLocked()
		return false
	}

	// Reconcile against near-duplicates / stale same-subject facts.
	switch outcome, target := fi.reconcileLocked(content, category, confidence); outcome {
	case reconcileReinforce:
		fi.reinforceLocked(target, sourceProject, confidence, provenance)
		fi.notifyChanged()
		fi.persistLocked()
		return false
	case reconcileSupersede:
		fi.logger.Debug("memory: superseding stale fact",
			zap.String("old_id", target.ID),
			zap.String("old", target.Content),
			zap.String("new", content),
		)
		delete(fi.facts, target.ID)
		// Tombstone the superseded fact or the shared-file merge re-adopts it
		// from disk and the update never sticks.
		fi.recordTombstonesLocked(target.ID)
		if provenance == "" {
			provenance = ProvenanceExtraction
		}
		provenance += " (supersedes " + target.ID + ")"
	}

	fact := &Fact{
		ID:            id,
		Content:       content,
		Category:      category,
		Tags:          tags,
		CreatedAt:     time.Now(),
		LastAccessed:  time.Now(),
		AccessCount:   1,
		Score:         1.0,
		SourceProject: sourceProject,
		Confidence:    confidence,
		Provenance:    provenance,
	}

	fi.facts[id] = fact

	// Enforce max facts limit — remove lowest-scoring facts
	if len(fi.facts) > fi.config.MaxFactsCount {
		fi.pruneLowestLocked(len(fi.facts) - fi.config.MaxFactsCount)
	}

	fi.notifyChanged()
	fi.persistLocked()
	return true
}

// reinforceLocked records a re-observation of a fact: bumps access/recency,
// backfills a missing source, and raises confidence toward the stronger signal
// plus a small increment (capped at 1.0). Must hold the write lock.
func (fi *FactIndex) reinforceLocked(f *Fact, sourceProject string, confidence float64, provenance string) {
	f.AccessCount++
	f.LastAccessed = time.Now()
	if f.SourceProject == "" && sourceProject != "" {
		f.SourceProject = sourceProject
	}
	base := f.confidence()
	if confidence > base {
		base = confidence
	}
	f.Confidence = math.Min(1.0, base+0.05)
	if provenance != "" && f.Provenance == "" {
		f.Provenance = provenance
	}
}

type reconcileOutcome int

const (
	reconcileNone reconcileOutcome = iota
	reconcileReinforce
	reconcileSupersede
)

// reconcileLocked compares new content against existing same-category facts and
// decides whether to reinforce a near-duplicate, supersede a stale same-subject
// fact, or do neither. Conservative by design: supersession needs both a shared
// subject and a new confidence at least as high as the target, so a weak guess
// can never wipe a stronger fact. Must hold at least a read lock.
func (fi *FactIndex) reconcileLocked(content, category string, confidence float64) (reconcileOutcome, *Fact) {
	newTokens := factTokenSet(content)
	if len(newTokens) == 0 {
		return reconcileNone, nil
	}
	var bestSupersede *Fact
	var bestSim float64
	for _, f := range fi.facts {
		if f.Category != category {
			continue
		}
		sim := jaccard(newTokens, factTokenSet(f.Content))
		if sim >= reconcileDuplicateJaccard {
			return reconcileReinforce, f // a rephrasing of an existing fact
		}
		if sim >= reconcileSupersedeJaccard && sim > bestSim && sharesSubject(content, f.Content) {
			bestSupersede, bestSim = f, sim
		}
	}
	if bestSupersede != nil && confidence >= bestSupersede.confidence() {
		return reconcileSupersede, bestSupersede
	}
	return reconcileNone, nil
}

// RemoveFact removes a fact by ID.
func (fi *FactIndex) RemoveFact(id string) bool {
	fi.mu.Lock()
	defer fi.mu.Unlock()

	if _, ok := fi.facts[id]; !ok {
		return false
	}
	delete(fi.facts, id)
	fi.recordTombstonesLocked(id)
	fi.persistLocked()
	return true
}

// ForgetMatching removes every fact whose content contains substr
// (case-insensitive) and returns the removed facts. It is the
// deterministic counterpart to AddFact, used by the /memory forget
// command and the @memory tool so the user/agent can correct or retract
// a learned fact without hand-editing JSON.
func (fi *FactIndex) ForgetMatching(substr string) []*Fact {
	substr = strings.ToLower(strings.TrimSpace(substr))
	if substr == "" {
		return nil
	}

	fi.mu.Lock()
	defer fi.mu.Unlock()

	var removed []*Fact
	var removedIDs []string
	for id, f := range fi.facts {
		if strings.Contains(strings.ToLower(f.Content), substr) {
			removed = append(removed, f)
			removedIDs = append(removedIDs, id)
			delete(fi.facts, id)
		}
	}
	if len(removed) > 0 {
		fi.recordTombstonesLocked(removedIDs...)
		fi.persistLocked()
	}
	return removed
}

// GetAll returns all facts sorted by score (descending).
func (fi *FactIndex) GetAll() []*Fact {
	fi.mu.RLock()
	defer fi.mu.RUnlock()

	return fi.sortedByScoreLocked()
}

// sortedByScoreLocked returns every fact ordered by temporal score descending,
// with a deterministic id tie-break so output never depends on Go's randomized
// map iteration order. Scores are computed purely (scoreOf) — never written
// back — so the caller may hold just a read lock.
func (fi *FactIndex) sortedByScoreLocked() []*Fact {
	now := time.Now()
	type scored struct {
		f *Fact
		s float64
	}
	list := make([]scored, 0, len(fi.facts))
	for _, f := range fi.facts {
		list = append(list, scored{f: f, s: fi.scoreOf(f, now)})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].s != list[j].s {
			return list[i].s > list[j].s
		}
		return list[i].f.ID < list[j].f.ID
	})
	facts := make([]*Fact, len(list))
	for i, it := range list {
		facts[i] = it.f
	}
	return facts
}

// GetByID returns a single fact by its content-hash id. The boolean
// signals presence so callers can distinguish "not stored" from a
// zero-value fact. Used by the HyDE retriever to lift cosine hits
// back into the keyword scorer.
func (fi *FactIndex) GetByID(id string) (*Fact, bool) {
	fi.mu.RLock()
	defer fi.mu.RUnlock()
	f, ok := fi.facts[id]
	return f, ok
}

// GetByCategory returns facts filtered by category.
func (fi *FactIndex) GetByCategory(category string) []*Fact {
	fi.mu.RLock()
	defer fi.mu.RUnlock()

	now := time.Now()
	var results []*Fact
	for _, f := range fi.facts {
		if f.Category == category {
			results = append(results, f)
		}
	}
	sort.Slice(results, func(i, j int) bool {
		return fi.scoreOf(results[i], now) > fi.scoreOf(results[j], now)
	})
	return results
}

// Search returns facts matching keywords, scored by relevance.
// keywords are matched against content and tags (case-insensitive).
func (fi *FactIndex) Search(keywords []string) []*Fact {
	if len(keywords) == 0 {
		return fi.GetAll()
	}

	fi.mu.RLock()
	defer fi.mu.RUnlock()

	now := time.Now()

	type scoredFact struct {
		fact     *Fact
		combined float64
	}

	var results []scoredFact
	for _, f := range fi.facts {
		rel := fi.computeRelevance(f, keywords)
		if rel > 0 {
			// Combined score: temporal score × relevance, both computed
			// purely so concurrent readers never write shared state.
			results = append(results, scoredFact{fact: f, combined: fi.scoreOf(f, now) * rel})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].combined > results[j].combined
	})

	facts := make([]*Fact, len(results))
	for i, r := range results {
		facts[i] = r.fact
	}
	return facts
}

// SearchBlended ranks facts by fusing three signals: semantic (cosine, passed
// in as id→score), lexical (keyword/tag overlap) and temporal (recency ×
// access). It is the ranking the HyDE path uses once a vector index is wired.
//
// Unlike Search (which multiplies temporal × lexical and is blind to cosine),
// the candidate set here is the UNION of keyword matches and semantic hits, so
// a fact found only by the vector store — the exact synonym/paraphrase case
// keyword search misses — can still rank. Each signal is min-max normalized
// across the candidates before the weighted sum (see blendCandidates), which is
// what keeps the weights meaningful and provider-agnostic.
//
// semantic may be nil (no vector index / disabled); the blend then degrades to
// lexical + temporal over the keyword matches. When both keywords and semantic
// are empty it falls back to all facts by temporal score, matching Search's
// empty-query behavior.
func (fi *FactIndex) SearchBlended(keywords []string, semantic map[string]float64, w RankWeights) []*Fact {
	return fi.SearchBlendedMin(keywords, semantic, w, 0)
}

// reinforcedLexicalHit is the raw computeRelevance score (before keyword
// normalization) that clears the lexical floor on its own: one content hit
// (1.0) reinforced by a tag hit (0.5), or two content hits.
const reinforcedLexicalHit = 1.5

// SearchBlendedMin is SearchBlended with a lexical relevance floor for
// facts found by keywords alone: the fact must either match at least
// minLexical of the keywords (computeRelevance scale, 1.0 = every keyword
// hit in the content) or carry a reinforced hit — a raw score of 1.5, i.e.
// a content hit backed by a tag hit, or two content hits — so a long
// natural-language query with one strong term still recalls its fact
// while one incidental token cannot. Semantic hits are unaffected (the
// vector floor is MinCosineScore, applied by the caller). A floor of 0
// keeps every keyword hit, which is what the pull tool wants.
func (fi *FactIndex) SearchBlendedMin(keywords []string, semantic map[string]float64, w RankWeights, minLexical float64) []*Fact {
	fi.mu.RLock()
	defer fi.mu.RUnlock()
	list := fi.blendedCandidatesLocked(keywords, semantic, w, minLexical)
	out := make([]*Fact, len(list))
	for i, c := range list {
		out[i] = c.fact
	}
	return out
}

// blendedCandidatesLocked is the ranking core shared by SearchBlendedMin
// and SearchBlendedMinRanked. Caller holds the read lock. An empty query
// yields every fact by temporal score as plain candidates.
func (fi *FactIndex) blendedCandidatesLocked(keywords []string, semantic map[string]float64, w RankWeights, minLexical float64) []*candidate {
	now := time.Now()
	w = w.normalized()

	cands := make(map[string]*candidate, len(semantic)+8)

	if len(keywords) > 0 {
		for _, f := range fi.facts {
			rel := fi.computeRelevance(f, keywords)
			if rel <= 0 {
				continue
			}
			if minLexical > 0 && rel < minLexical && rel*float64(len(keywords)) < reinforcedLexicalHit {
				continue
			}
			cands[f.ID] = &candidate{fact: f, lexical: rel, temporal: fi.scoreOf(f, now)}
		}
	}

	for id, sem := range semantic {
		f, ok := fi.facts[id]
		if !ok {
			continue // vector for an archived/forgotten fact — skip
		}
		if c, exists := cands[id]; exists {
			c.semantic = sem
			continue
		}
		cands[id] = &candidate{fact: f, semantic: sem, temporal: fi.scoreOf(f, now)}
	}

	if len(cands) == 0 {
		if len(keywords) == 0 && len(semantic) == 0 {
			all := fi.sortedByScoreLocked()
			list := make([]*candidate, len(all))
			for i, f := range all {
				list[i] = &candidate{fact: f, temporal: fi.scoreOf(f, now), final: fi.scoreOf(f, now)}
			}
			return list
		}
		return nil
	}

	list := make([]*candidate, 0, len(cands))
	for _, c := range cands {
		list = append(list, c)
	}
	blendCandidates(list, w)
	if len(list) == 1 {
		// Min-max normalization over one candidate is all zeros; a lone
		// hit is by definition the top of its ranking.
		list[0].final = 1
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].final != list[j].final {
			return list[i].final > list[j].final
		}
		return list[i].fact.ID < list[j].fact.ID // deterministic tie-break
	})
	return list
}

// MarkAccessed updates access metadata for retrieved facts. Bumps are
// debounced: they fold into the next real persist, or flush when the
// interval elapses — a bare access is not worth a full-file rewrite per
// retrieval (see accessFlushInterval).
func (fi *FactIndex) MarkAccessed(ids []string) {
	fi.mu.Lock()
	defer fi.mu.Unlock()

	changed := false
	for _, id := range ids {
		if f, ok := fi.facts[id]; ok {
			f.AccessCount++
			f.LastAccessed = time.Now()
			changed = true
		}
	}
	if !changed {
		return
	}
	if time.Since(fi.lastAccessFlush) >= fi.accessFlushInterval {
		fi.persistLocked()
		return
	}
	fi.accessDirty = true
}

// Count returns the number of stored facts.
func (fi *FactIndex) Count() int {
	fi.mu.RLock()
	defer fi.mu.RUnlock()
	return len(fi.facts)
}

// ReplaceFacts replaces the entire fact set (used by compaction). Facts
// dropped by the replacement are tombstoned so the shared-file merge (and the
// other process) honors the consolidation instead of resurrecting them.
func (fi *FactIndex) ReplaceFacts(facts []*Fact) {
	fi.mu.Lock()
	defer fi.mu.Unlock()

	next := make(map[string]*Fact, len(facts))
	for _, f := range facts {
		next[f.ID] = f
	}
	var droppedIDs []string
	for id := range fi.facts {
		if _, kept := next[id]; !kept {
			droppedIDs = append(droppedIDs, id)
		}
	}
	fi.facts = next
	fi.recordTombstonesLocked(droppedIDs...)
	// The removal hook covers the dropped side; the consolidated facts that
	// replaced them are a content change of their own.
	fi.notifyChanged()
	fi.persistLocked()
}

// GetArchiveCandidates returns facts with score below threshold.
func (fi *FactIndex) GetArchiveCandidates(threshold float64) []*Fact {
	fi.mu.RLock()
	defer fi.mu.RUnlock()

	now := time.Now()
	var candidates []*Fact
	for _, f := range fi.facts {
		if fi.scoreOf(f, now) < threshold {
			candidates = append(candidates, f)
		}
	}
	return candidates
}

// appendFactsToArchive appends facts to the JSON archive at archivePath,
// creating it when absent. It never removes anything — pure append, so a
// fact archived twice is duplicated rather than risk being lost.
func appendFactsToArchive(facts []*Fact, archivePath string) error {
	if len(facts) == 0 {
		return nil
	}
	var archive []*Fact
	if data, err := os.ReadFile(archivePath); err == nil { //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
		_ = json.Unmarshal(data, &archive)
	}
	archive = append(archive, facts...)

	data, err := json.MarshalIndent(archive, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(archivePath, data, 0o600)
}

// ArchiveFacts moves low-scoring facts to an archive file and removes them from the index.
func (fi *FactIndex) ArchiveFacts(facts []*Fact, archivePath string) error {
	if len(facts) == 0 {
		return nil
	}
	if err := appendFactsToArchive(facts, archivePath); err != nil {
		return err
	}

	// Remove from index
	fi.mu.Lock()
	defer fi.mu.Unlock()
	ids := make([]string, 0, len(facts))
	for _, f := range facts {
		delete(fi.facts, f.ID)
		ids = append(ids, f.ID)
	}
	fi.recordTombstonesLocked(ids...)
	fi.persistLocked()
	return nil
}

// GenerateMarkdown renders the top facts as human-readable markdown grouped by category.
func (fi *FactIndex) GenerateMarkdown(maxSize int) string {
	facts := fi.GetAll()
	if len(facts) == 0 {
		return ""
	}

	// Group by category
	categories := make(map[string][]*Fact)
	var catOrder []string
	for _, f := range facts {
		cat := f.Category
		if cat == "" {
			cat = "general"
		}
		if _, exists := categories[cat]; !exists {
			catOrder = append(catOrder, cat)
		}
		categories[cat] = append(categories[cat], f)
	}
	sort.Strings(catOrder)

	var sb strings.Builder
	sb.WriteString("# Long-term Memory\n\n")

	for _, cat := range catOrder {
		catFacts := categories[cat]
		sb.WriteString(fmt.Sprintf("## %s\n\n", cases.Title(language.English).String(cat)))
		for _, f := range catFacts {
			line := fmt.Sprintf("- %s\n", f.Content)
			if sb.Len()+len(line) > maxSize {
				sb.WriteString("- ...(truncated)\n")
				return sb.String()
			}
			sb.WriteString(line)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// --- internal ---

func (fi *FactIndex) hashContent(content string) string {
	normalized := strings.ToLower(strings.TrimSpace(content))
	h := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("%x", h[:12]) // 24 hex chars — enough for uniqueness
}

// reconcileStopwords are dropped before similarity so connective words
// ("the", "via", "de", "para") cannot inflate the overlap of unrelated facts.
var reconcileStopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "of": true,
	"to": true, "for": true, "in": true, "on": true, "with": true, "via": true,
	"is": true, "are": true, "was": true, "uses": true, "use": true, "user": true,
	"de": true, "da": true, "do": true, "para": true, "com": true, "que": true,
	"em": true, "no": true, "na": true, "usa": true, "usuario": true,
}

// sigTokens returns the lowercased significant tokens of content in order:
// letter/digit runs of at least 3 runes that are not stopwords. Tokenization
// is Unicode-aware — an ASCII-only splitter shreds accented Portuguese words
// ("configuração" → "configura" + debris), silently degrading the Jaccard
// dedupe/supersede similarity for exactly the language most facts arrive in.
func sigTokens(content string) []string {
	fields := strings.FieldsFunc(strings.ToLower(content), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, t := range fields {
		if utf8.RuneCountInString(t) < 3 || reconcileStopwords[t] {
			continue
		}
		out = append(out, t)
	}
	return out
}

func factTokenSet(content string) map[string]bool {
	toks := sigTokens(content)
	set := make(map[string]bool, len(toks))
	for _, t := range toks {
		set[t] = true
	}
	return set
}

// jaccard is the size of the intersection over the union of two token sets.
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if b[t] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// sharesSubject reports whether two facts open on the same subject: their first
// up to two significant tokens match. This gates supersession so that two facts
// merely sharing some vocabulary are not mistaken for an update of one another.
func sharesSubject(a, b string) bool {
	ta, tb := sigTokens(a), sigTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	n := 2
	if len(ta) < n {
		n = len(ta)
	}
	if len(tb) < n {
		n = len(tb)
	}
	for i := 0; i < n; i++ {
		if ta[i] != tb[i] {
			return false
		}
	}
	return true
}

// minRunesForPrefixMatch is the shortest keyword allowed to match as a token
// PREFIX ("compress" → "compression"). Shorter keywords match tokens exactly
// only — raw substring matching let 2-3 letter keywords fire inside unrelated
// words ("go" inside "django"), polluting retrieval with noise facts that the
// access boost then entrenched.
const minRunesForPrefixMatch = 4

func (fi *FactIndex) computeRelevance(f *Fact, keywords []string) float64 {
	contentToks := allTokens(f.Content)
	tagToks := allTokens(strings.Join(f.Tags, " "))

	// Keywords are weighted by their rarity across the fact index (an
	// IDF-style weight normalized around 1.0 and clamped), so a term that
	// appears in half the facts no longer counts as much as a term that
	// names one of them. The weights are normalized back to the
	// keyword-count scale the lexical floors are defined on.
	weights := fi.keywordWeightsLocked(keywords)
	var score, total float64
	for i, kw := range keywords {
		kwLower := strings.ToLower(strings.TrimSpace(kw))
		if kwLower == "" {
			continue
		}
		w := weights[i]
		total += w
		if anyTokenMatches(contentToks, kwLower) {
			score += w
		}
		if anyTokenMatches(tagToks, kwLower) {
			score += 0.5 * w
		}
	}
	if total == 0 {
		return 0
	}
	// Normalize by the (weighted) number of keywords
	return score / total
}

// keywordWeightsLocked returns one IDF-style weight per keyword: log-scaled
// rarity over the facts' token vocabulary, normalized so the mean is 1 and
// clamped to [0.5, 2]. Facts unseen by the vocabulary (few facts, empty
// index) weigh 1. The vocabulary is rebuilt lazily when the index changed.
func (fi *FactIndex) keywordWeightsLocked(keywords []string) []float64 {
	w := make([]float64, len(keywords))
	for i := range w {
		w[i] = 1
	}
	n := len(fi.facts)
	if n < 4 {
		return w
	}
	df := fi.docFreqLocked()
	var sum float64
	var count int
	for i, kw := range keywords {
		kwLower := strings.ToLower(strings.TrimSpace(kw))
		if kwLower == "" {
			continue
		}
		hits := df[kwLower]
		if hits == 0 && utf8.RuneCountInString(kwLower) >= minRunesForPrefixMatch {
			for tok, c := range df {
				if strings.HasPrefix(tok, kwLower) {
					hits += c
				}
			}
		}
		w[i] = math.Log(1 + float64(n)/float64(hits+1))
		sum += w[i]
		count++
	}
	if count == 0 || sum == 0 {
		return w
	}
	mean := sum / float64(count)
	for i := range w {
		v := w[i] / mean
		if v < 0.5 {
			v = 0.5
		} else if v > 2 {
			v = 2
		}
		w[i] = v
	}
	return w
}

// docFreqLocked returns token → number of facts containing it, cached per
// index revision (every mutation bumps fi.rev in persistLocked). The cache
// has its own mutex: relevance runs under the index read lock, which
// several searches hold at once, so the cache write must not race.
func (fi *FactIndex) docFreqLocked() map[string]int {
	fi.dfMu.Lock()
	defer fi.dfMu.Unlock()
	if fi.dfCache != nil && fi.dfRev == fi.rev && fi.dfCount == len(fi.facts) {
		return fi.dfCache
	}
	df := make(map[string]int, 256)
	for _, f := range fi.facts {
		seen := map[string]bool{}
		for _, tok := range allTokens(f.Content + " " + strings.Join(f.Tags, " ")) {
			if !seen[tok] {
				seen[tok] = true
				df[tok]++
			}
		}
	}
	fi.dfCache, fi.dfRev, fi.dfCount = df, fi.rev, len(fi.facts)
	return df
}

// allTokens returns every lowercased letter/digit run of content — unfiltered
// (no stopword/length cut), because filtering belongs to keyword EXTRACTION;
// the matching side must be able to satisfy whatever keyword arrives.
func allTokens(content string) []string {
	return strings.FieldsFunc(strings.ToLower(content), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// anyTokenMatches reports whether kw matches any token: exactly, or as a
// prefix when the keyword is long enough to be discriminating.
func anyTokenMatches(tokens []string, kw string) bool {
	prefixOK := utf8.RuneCountInString(kw) >= minRunesForPrefixMatch
	for _, t := range tokens {
		if t == kw {
			return true
		}
		if prefixOK && strings.HasPrefix(t, kw) {
			return true
		}
	}
	return false
}

// scoreOf computes a fact's temporal score PURELY — no shared state is
// written, so any number of readers may call it concurrently under the read
// lock. The stored Fact.Score field is refreshed only at persist time (write
// lock held) as a human-readable annotation in the JSON; ranking never reads
// the stored field.
func (fi *FactIndex) scoreOf(f *Fact, now time.Time) float64 {
	halfLife := fi.config.DecayHalfLifeDays
	if halfLife <= 0 {
		halfLife = 30.0
	}
	daysSinceAccess := now.Sub(f.LastAccessed).Hours() / 24.0
	if daysSinceAccess < 0 {
		daysSinceAccess = 0
	}
	accessBoost := 1.0 + math.Log1p(float64(f.AccessCount))
	decay := math.Exp(-daysSinceAccess * math.Ln2 / halfLife)
	// Confidence ∈ (0,1] scales the score into (0.5, 1.5]×, so a trusted
	// fact outranks a low-confidence guess of equal recency and survives
	// decay and pruning longer.
	return accessBoost * decay * (0.5 + f.confidence())
}

// recalcScoresLocked refreshes every fact's stored Score annotation. It
// MUTATES shared state, so the caller must hold the WRITE lock — it is called
// only from persistLocked, purely so the persisted JSON carries current
// scores for human inspection.
func (fi *FactIndex) recalcScoresLocked() {
	now := time.Now()
	for _, f := range fi.facts {
		f.Score = fi.scoreOf(f, now)
	}
}

// pruneLowestLocked removes the N lowest-scoring facts. Must hold write lock.
func (fi *FactIndex) pruneLowestLocked(n int) {
	now := time.Now()

	type idScore struct {
		id    string
		score float64
	}
	all := make([]idScore, 0, len(fi.facts))
	for id, f := range fi.facts {
		all = append(all, idScore{id: id, score: fi.scoreOf(f, now)})
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].score < all[j].score
	})

	pruned := make([]string, 0, n)
	for i := 0; i < n && i < len(all); i++ {
		fi.logger.Debug("pruning low-score fact",
			zap.String("id", all[i].id),
			zap.Float64("score", all[i].score))
		delete(fi.facts, all[i].id)
		pruned = append(pruned, all[i].id)
	}
	// Tombstone so the shared-file merge doesn't re-adopt what the cap just
	// evicted (and the other process converges on the same eviction).
	fi.recordTombstonesLocked(pruned...)
}

func (fi *FactIndex) load() {
	data, err := readStoreFile(fi.path)
	if fi.latch.lockIfSealed(err, fi.logger, "facts") {
		return
	}
	if err != nil {
		if !os.IsNotExist(err) {
			fi.logger.Debug("failed to load fact index", zap.Error(err))
		}
		return
	}

	var facts []*Fact
	if err := json.Unmarshal(data, &facts); err != nil {
		// Quarantine, never leave in place: an unparseable index left under
		// the live name gets overwritten by the next persist, silently erasing
		// every accumulated fact. Moving it aside keeps the bytes recoverable.
		if qpath, qerr := quarantineCorrupt(fi.path); qerr == nil {
			fi.logger.Warn("fact index corrupt; quarantined for recovery",
				zap.String("quarantine", qpath), zap.Error(err))
		} else {
			fi.logger.Warn("fact index corrupt and quarantine failed; refusing to start empty over it",
				zap.Error(qerr))
		}
		return
	}

	for _, f := range facts {
		fi.facts[f.ID] = f
	}

	// One-time, idempotent enrichment of facts saved before confidence existed:
	// give each a confidence derived from how often it was re-observed and a
	// "legacy" provenance, then rewrite the index once. Non-destructive — no
	// fact is removed, and facts that already have a confidence are skipped, so
	// later loads are no-ops.
	if fi.backfillLegacyConfidenceLocked() {
		fi.persistLocked()
		fi.logger.Info("memory: enriched legacy facts with confidence/provenance")
	}

	fi.logger.Debug("loaded fact index", zap.Int("count", len(fi.facts)))
}

// backfillLegacyConfidenceLocked assigns confidence/provenance to pre-confidence
// facts in place and reports whether anything changed. Caller holds the write
// lock (or, as in load, runs single-threaded during construction).
func (fi *FactIndex) backfillLegacyConfidenceLocked() bool {
	changed := false
	for _, f := range fi.facts {
		if f.Confidence > 0 {
			continue // already has a confidence — leave it (idempotent)
		}
		f.Confidence = legacyConfidence(f.AccessCount)
		if f.Provenance == "" {
			f.Provenance = ProvenanceLegacy
		}
		changed = true
	}
	return changed
}

// legacyConfidence maps a legacy fact's re-observation count to a confidence in
// [0.5, 0.85]: a fact the user kept hitting is more trustworthy, but without a
// known source it never reaches the level of a freshly user-stated fact.
func legacyConfidence(accessCount int) float64 {
	c := 0.5 + 0.1*math.Log1p(float64(accessCount))
	if c < 0.5 {
		c = 0.5
	}
	if c > 0.85 {
		c = 0.85
	}
	return c
}

func (fi *FactIndex) persistLocked() {
	fi.rev++
	if fi.latch.locked() {
		return // sealed file we cannot read: never overwrite it
	}
	// Reconcile with the shared file first: adopt facts the other process
	// persisted and honor tombstones from either side, so a rewrite never
	// erases the other process's learning.
	fi.mergeFromDiskLocked()
	// Refresh the persisted Score annotations (write lock is held here).
	fi.recalcScoresLocked()

	facts := make([]*Fact, 0, len(fi.facts))
	for _, f := range fi.facts {
		facts = append(facts, f)
	}

	// Sort by score descending for readable JSON
	sort.Slice(facts, func(i, j int) bool {
		return facts[i].Score > facts[j].Score
	})

	data, err := json.MarshalIndent(facts, "", "  ")
	if err != nil {
		fi.logger.Warn("failed to marshal fact index", zap.Error(err))
		return
	}

	if err := atomicWriteFile(fi.path, data, 0o600); err != nil {
		fi.logger.Warn("failed to write fact index", zap.Error(err))
	}
	fi.lastAccessFlush = time.Now()
	fi.accessDirty = false
}

// mergeFromDiskLocked reconciles the in-memory map with the shared on-disk
// state before a rewrite:
//
//  1. Tombstones are unioned from the sidecar (deletions made by the other
//     process) and applied — any copy whose CreatedAt predates its tombstone
//     is dropped, here and from the write that follows.
//  2. Facts present on disk but not in memory (learned by the other process)
//     are adopted; facts known to both keep the freshest access metadata and
//     the highest confidence.
//
// Read failures leave the current view untouched — worst case is the old
// last-writer-wins behavior for one cycle. Caller must hold the write lock.
func (fi *FactIndex) mergeFromDiskLocked() {
	fi.loadTombstonesLocked()
	for id, ts := range fi.tombstones {
		if f, ok := fi.facts[id]; ok && ts.After(f.CreatedAt) {
			delete(fi.facts, id)
		}
	}

	data, err := readStoreFile(fi.path)
	if fi.latch.lockIfSealed(err, fi.logger, "facts") || err != nil {
		return
	}
	var onDisk []*Fact
	if err := json.Unmarshal(data, &onDisk); err != nil {
		return
	}
	for _, df := range onDisk {
		if df == nil || df.ID == "" {
			continue
		}
		if ts, dead := fi.tombstones[df.ID]; dead && ts.After(df.CreatedAt) {
			continue
		}
		cur, ok := fi.facts[df.ID]
		if !ok {
			fi.facts[df.ID] = df
			continue
		}
		if df.LastAccessed.After(cur.LastAccessed) {
			cur.LastAccessed = df.LastAccessed
		}
		if df.AccessCount > cur.AccessCount {
			cur.AccessCount = df.AccessCount
		}
		if df.Confidence > cur.Confidence {
			cur.Confidence = df.Confidence
		}
	}
	if excess := len(fi.facts) - fi.config.MaxFactsCount; excess > 0 {
		fi.pruneLowestLocked(excess)
	}
}

// SetOnRemoved registers the default removal hook. Pass nil to detach.
// Kept for API stability — it delegates to SetRemovalHook under a fixed
// key, so it composes with keyed consumers instead of clobbering them.
func (fi *FactIndex) SetOnRemoved(fn func(ids []string)) {
	fi.SetRemovalHook("default", fn)
}

// SetRemovalHook registers (fn != nil) or removes (fn == nil) a keyed
// removal hook (see the removalHooks field doc). Not safe to call
// concurrently with index operations — wire hooks at construction/attach
// time.
func (fi *FactIndex) SetRemovalHook(key string, fn func(ids []string)) {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	if fn == nil {
		delete(fi.removalHooks, key)
		return
	}
	if fi.removalHooks == nil {
		fi.removalHooks = make(map[string]func(ids []string))
	}
	fi.removalHooks[key] = fn
}

// recordTombstonesLocked marks ids as explicitly deleted, persists the
// sidecar so the deletion propagates to the other process, and notifies the
// removal hook so derived state (vectors) is dropped in lockstep. This is the
// single chokepoint every deletion path goes through. Caller must hold the
// write lock.
func (fi *FactIndex) recordTombstonesLocked(ids ...string) {
	if len(ids) == 0 {
		return
	}
	if len(fi.removalHooks) > 0 {
		keys := make([]string, 0, len(fi.removalHooks))
		for k := range fi.removalHooks {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fi.removalHooks[k](ids)
		}
	}
	now := time.Now()
	for _, id := range ids {
		fi.tombstones[id] = now
	}
	fi.pruneTombstonesLocked(now)
	data, err := json.MarshalIndent(fi.tombstones, "", "  ")
	if err != nil {
		return
	}
	if err := atomicWriteFile(fi.tombstonePath, data, 0o600); err != nil {
		fi.logger.Warn("failed to persist fact tombstones", zap.Error(err))
	}
}

// loadTombstonesLocked unions the sidecar's tombstones into memory (the other
// process may have recorded deletions since our last read).
func (fi *FactIndex) loadTombstonesLocked() {
	data, err := readStoreFile(fi.tombstonePath)
	if fi.latch.lockIfSealed(err, fi.logger, "facts") || err != nil {
		return
	}
	var onDisk map[string]time.Time
	if err := json.Unmarshal(data, &onDisk); err != nil {
		return
	}
	for id, ts := range onDisk {
		if cur, ok := fi.tombstones[id]; !ok || ts.After(cur) {
			fi.tombstones[id] = ts
		}
	}
	fi.pruneTombstonesLocked(time.Now())
}

// pruneTombstonesLocked drops deletion markers past retention so the sidecar
// stays bounded. Caller must hold the write lock.
func (fi *FactIndex) pruneTombstonesLocked(now time.Time) {
	cutoff := now.Add(-tombstoneRetention)
	for id, ts := range fi.tombstones {
		if ts.Before(cutoff) {
			delete(fi.tombstones, id)
		}
	}
}
