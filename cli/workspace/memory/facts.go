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

	"go.uber.org/zap"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// FactIndex manages scored long-term memory facts with JSON persistence.
type FactIndex struct {
	facts  map[string]*Fact // keyed by ID
	mu     sync.RWMutex
	path   string // path to memory_index.json
	logger *zap.Logger
	config Config
}

// NewFactIndex creates a new fact index.
func NewFactIndex(memoryDir string, config Config, logger *zap.Logger) *FactIndex {
	fi := &FactIndex{
		facts:  make(map[string]*Fact),
		path:   fmt.Sprintf("%s/memory_index.json", memoryDir),
		logger: logger,
		config: config,
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
		fi.persistLocked()
		return false
	}

	// Reconcile against near-duplicates / stale same-subject facts.
	switch outcome, target := fi.reconcileLocked(content, category, confidence); outcome {
	case reconcileReinforce:
		fi.reinforceLocked(target, sourceProject, confidence, provenance)
		fi.persistLocked()
		return false
	case reconcileSupersede:
		fi.logger.Debug("memory: superseding stale fact",
			zap.String("old_id", target.ID),
			zap.String("old", target.Content),
			zap.String("new", content),
		)
		delete(fi.facts, target.ID)
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
	for id, f := range fi.facts {
		if strings.Contains(strings.ToLower(f.Content), substr) {
			removed = append(removed, f)
			delete(fi.facts, id)
		}
	}
	if len(removed) > 0 {
		fi.persistLocked()
	}
	return removed
}

// GetAll returns all facts sorted by score (descending).
func (fi *FactIndex) GetAll() []*Fact {
	fi.mu.RLock()
	defer fi.mu.RUnlock()

	fi.recalcScoresLocked()
	return fi.sortedByScoreLocked()
}

// sortedByScoreLocked returns every fact ordered by temporal score descending,
// with a deterministic id tie-break so output never depends on Go's randomized
// map iteration order. The caller must hold at least a read lock and is
// responsible for calling recalcScoresLocked first if fresh scores are needed.
func (fi *FactIndex) sortedByScoreLocked() []*Fact {
	facts := make([]*Fact, 0, len(fi.facts))
	for _, f := range fi.facts {
		facts = append(facts, f)
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].Score != facts[j].Score {
			return facts[i].Score > facts[j].Score
		}
		return facts[i].ID < facts[j].ID
	})
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

	fi.recalcScoresLocked()
	var results []*Fact
	for _, f := range fi.facts {
		if f.Category == category {
			results = append(results, f)
		}
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
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

	fi.recalcScoresLocked()

	type scoredFact struct {
		fact      *Fact
		relevance float64
	}

	var results []scoredFact
	for _, f := range fi.facts {
		rel := fi.computeRelevance(f, keywords)
		if rel > 0 {
			results = append(results, scoredFact{fact: f, relevance: rel})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		// Combined score: temporal score * relevance
		si := results[i].fact.Score * results[i].relevance
		sj := results[j].fact.Score * results[j].relevance
		return si > sj
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
	fi.mu.RLock()
	defer fi.mu.RUnlock()

	fi.recalcScoresLocked()
	w = w.normalized()

	cands := make(map[string]*candidate, len(semantic)+8)

	if len(keywords) > 0 {
		for _, f := range fi.facts {
			if rel := fi.computeRelevance(f, keywords); rel > 0 {
				cands[f.ID] = &candidate{fact: f, lexical: rel, temporal: f.Score}
			}
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
		cands[id] = &candidate{fact: f, semantic: sem, temporal: f.Score}
	}

	if len(cands) == 0 {
		if len(keywords) == 0 && len(semantic) == 0 {
			return fi.sortedByScoreLocked()
		}
		return nil
	}

	list := make([]*candidate, 0, len(cands))
	for _, c := range cands {
		list = append(list, c)
	}
	blendCandidates(list, w)

	sort.Slice(list, func(i, j int) bool {
		if list[i].final != list[j].final {
			return list[i].final > list[j].final
		}
		return list[i].fact.ID < list[j].fact.ID // deterministic tie-break
	})

	out := make([]*Fact, len(list))
	for i, c := range list {
		out[i] = c.fact
	}
	return out
}

// MarkAccessed updates access metadata for retrieved facts.
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
	if changed {
		fi.persistLocked()
	}
}

// Count returns the number of stored facts.
func (fi *FactIndex) Count() int {
	fi.mu.RLock()
	defer fi.mu.RUnlock()
	return len(fi.facts)
}

// ReplaceFacts replaces the entire fact set (used by compaction).
func (fi *FactIndex) ReplaceFacts(facts []*Fact) {
	fi.mu.Lock()
	defer fi.mu.Unlock()

	fi.facts = make(map[string]*Fact, len(facts))
	for _, f := range facts {
		fi.facts[f.ID] = f
	}
	fi.persistLocked()
}

// GetArchiveCandidates returns facts with score below threshold.
func (fi *FactIndex) GetArchiveCandidates(threshold float64) []*Fact {
	fi.mu.RLock()
	defer fi.mu.RUnlock()

	fi.recalcScoresLocked()
	var candidates []*Fact
	for _, f := range fi.facts {
		if f.Score < threshold {
			candidates = append(candidates, f)
		}
	}
	return candidates
}

// ArchiveFacts moves low-scoring facts to an archive file and removes them from the index.
func (fi *FactIndex) ArchiveFacts(facts []*Fact, archivePath string) error {
	if len(facts) == 0 {
		return nil
	}

	// Read existing archive
	var archive []*Fact
	if data, err := os.ReadFile(archivePath); err == nil { //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
		_ = json.Unmarshal(data, &archive)
	}
	archive = append(archive, facts...)

	data, err := json.MarshalIndent(archive, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(archivePath, data, 0o600); err != nil {
		return err
	}

	// Remove from index
	fi.mu.Lock()
	defer fi.mu.Unlock()
	for _, f := range facts {
		delete(fi.facts, f.ID)
	}
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
// alphanumeric runs of length ≥ 3 that are not stopwords.
func sigTokens(content string) []string {
	fields := strings.FieldsFunc(strings.ToLower(content), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	out := make([]string, 0, len(fields))
	for _, t := range fields {
		if len(t) < 3 || reconcileStopwords[t] {
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

func (fi *FactIndex) computeRelevance(f *Fact, keywords []string) float64 {
	contentLower := strings.ToLower(f.Content)
	tagsJoined := strings.ToLower(strings.Join(f.Tags, " "))

	var score float64
	for _, kw := range keywords {
		kwLower := strings.ToLower(kw)
		if strings.Contains(contentLower, kwLower) {
			score += 1.0
		}
		if strings.Contains(tagsJoined, kwLower) {
			score += 0.5
		}
	}
	// Normalize by number of keywords
	return score / float64(len(keywords))
}

// recalcScoresLocked recalculates temporal scores for all facts.
// Must be called with at least a read lock held.
func (fi *FactIndex) recalcScoresLocked() {
	halfLife := fi.config.DecayHalfLifeDays
	if halfLife <= 0 {
		halfLife = 30.0
	}
	now := time.Now()

	for _, f := range fi.facts {
		daysSinceAccess := now.Sub(f.LastAccessed).Hours() / 24.0
		if daysSinceAccess < 0 {
			daysSinceAccess = 0
		}
		accessBoost := 1.0 + math.Log1p(float64(f.AccessCount))
		decay := math.Exp(-daysSinceAccess * math.Ln2 / halfLife)
		// Confidence ∈ (0,1] scales the score into (0.5, 1.5]×, so a trusted
		// fact outranks a low-confidence guess of equal recency and survives
		// decay and pruning longer.
		f.Score = accessBoost * decay * (0.5 + f.confidence())
	}
}

// pruneLowestLocked removes the N lowest-scoring facts. Must hold write lock.
func (fi *FactIndex) pruneLowestLocked(n int) {
	fi.recalcScoresLocked()

	type idScore struct {
		id    string
		score float64
	}
	all := make([]idScore, 0, len(fi.facts))
	for id, f := range fi.facts {
		all = append(all, idScore{id: id, score: f.Score})
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].score < all[j].score
	})

	for i := 0; i < n && i < len(all); i++ {
		fi.logger.Debug("pruning low-score fact",
			zap.String("id", all[i].id),
			zap.Float64("score", all[i].score))
		delete(fi.facts, all[i].id)
	}
}

func (fi *FactIndex) load() {
	data, err := os.ReadFile(fi.path)
	if err != nil {
		if !os.IsNotExist(err) {
			fi.logger.Debug("failed to load fact index", zap.Error(err))
		}
		return
	}

	var facts []*Fact
	if err := json.Unmarshal(data, &facts); err != nil {
		fi.logger.Warn("failed to parse fact index", zap.Error(err))
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

	if err := os.WriteFile(fi.path, data, 0o600); err != nil {
		fi.logger.Warn("failed to write fact index", zap.Error(err))
	}
}
