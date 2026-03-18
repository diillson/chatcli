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
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}

	id := fi.hashContent(content)

	fi.mu.Lock()
	defer fi.mu.Unlock()

	if existing, ok := fi.facts[id]; ok {
		// Fact already exists — bump access count and update timestamp
		existing.AccessCount++
		existing.LastAccessed = time.Now()
		fi.persistLocked()
		return false
	}

	fact := &Fact{
		ID:           id,
		Content:      content,
		Category:     category,
		Tags:         tags,
		CreatedAt:    time.Now(),
		LastAccessed: time.Now(),
		AccessCount:  1,
		Score:        1.0,
	}

	fi.facts[id] = fact

	// Enforce max facts limit — remove lowest-scoring facts
	if len(fi.facts) > fi.config.MaxFactsCount {
		fi.pruneLowestLocked(len(fi.facts) - fi.config.MaxFactsCount)
	}

	fi.persistLocked()
	return true
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

// GetAll returns all facts sorted by score (descending).
func (fi *FactIndex) GetAll() []*Fact {
	fi.mu.RLock()
	defer fi.mu.RUnlock()

	fi.recalcScoresLocked()
	facts := make([]*Fact, 0, len(fi.facts))
	for _, f := range fi.facts {
		facts = append(facts, f)
	}
	sort.Slice(facts, func(i, j int) bool {
		return facts[i].Score > facts[j].Score
	})
	return facts
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
		fact     *Fact
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
	if data, err := os.ReadFile(archivePath); err == nil {
		_ = json.Unmarshal(data, &archive)
	}
	archive = append(archive, facts...)

	data, err := json.MarshalIndent(archive, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(archivePath, data, 0o644); err != nil {
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
		f.Score = accessBoost * decay
	}
}

// pruneLowestLocked removes the N lowest-scoring facts. Must hold write lock.
func (fi *FactIndex) pruneLowestLocked(n int) {
	fi.recalcScoresLocked()

	type idScore struct {
		id    string
		score float64
	}
	var all []idScore
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
	fi.logger.Debug("loaded fact index", zap.Int("count", len(fi.facts)))
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

	if err := os.WriteFile(fi.path, data, 0o644); err != nil {
		fi.logger.Warn("failed to write fact index", zap.Error(err))
	}
}
