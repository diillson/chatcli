package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// compactionMinKeepRatio is the shrink guard on LLM-assisted consolidation: a
// result keeping fewer than this fraction of the original facts is treated as
// truncated/defective model output and rejected (score-based curation runs
// instead). Models cut long lists routinely; before this guard a truncated
// answer was applied wholesale, permanently deleting every fact the model
// failed to echo.
const compactionMinKeepRatio = 0.5

// compactorStateFile persists the last-compaction timestamp so a process
// restart does not re-trigger the (LLM-billed, destructive-adjacent)
// consolidation pass that just ran.
const compactorStateFile = "compactor_state.json"

// Compactor handles LLM-based memory consolidation and cleanup.
type Compactor struct {
	facts  *FactIndex
	daily  *DailyNoteStore
	config Config
	logger *zap.Logger
	memDir string

	lastCompaction time.Time
}

// compactorState is the on-disk shape of the compactor's durable state.
type compactorState struct {
	LastCompaction time.Time `json:"last_compaction"`
}

// NewCompactor creates a new memory compactor, restoring the last-compaction
// timestamp from the previous process when present.
func NewCompactor(facts *FactIndex, daily *DailyNoteStore, config Config, memDir string, logger *zap.Logger) *Compactor {
	c := &Compactor{
		facts:  facts,
		daily:  daily,
		config: config,
		logger: logger,
		memDir: memDir,
	}
	c.loadState()
	return c
}

// loadState restores lastCompaction from disk. Any read/parse failure just
// leaves the zero value — the worst case is one extra compaction check.
func (c *Compactor) loadState() {
	data, err := os.ReadFile(filepath.Join(c.memDir, compactorStateFile))
	if err != nil {
		return
	}
	var s compactorState
	if err := json.Unmarshal(data, &s); err != nil {
		return
	}
	c.lastCompaction = s.LastCompaction
}

// markCompacted records a completed compaction in memory and on disk.
func (c *Compactor) markCompacted() {
	c.lastCompaction = time.Now()
	data, err := json.Marshal(compactorState{LastCompaction: c.lastCompaction})
	if err != nil {
		return
	}
	if err := atomicWriteFile(filepath.Join(c.memDir, compactorStateFile), data, 0o600); err != nil {
		c.logger.Warn("failed to persist compactor state", zap.Error(err))
	}
}

// NeedsCompaction checks if compaction should run.
func (c *Compactor) NeedsCompaction() bool {
	interval := time.Duration(c.config.CompactionInterval) * time.Hour
	if interval <= 0 {
		interval = 24 * time.Hour
	}

	if time.Since(c.lastCompaction) < interval {
		return false
	}

	// Also trigger if fact count exceeds 80% of max
	if c.facts.Count() > int(float64(c.config.MaxFactsCount)*0.8) {
		return true
	}

	return time.Since(c.lastCompaction) >= interval
}

// RunWithLLM performs LLM-assisted compaction.
func (c *Compactor) RunWithLLM(ctx context.Context, sendPrompt func(ctx context.Context, prompt string) (string, error)) error {
	c.logger.Info("Starting memory compaction (LLM-assisted)")

	facts := c.facts.GetAll()
	if len(facts) < 10 {
		c.logger.Debug("Too few facts for compaction", zap.Int("count", len(facts)))
		c.markCompacted()
		return nil
	}

	// Build fact list for LLM
	var sb strings.Builder
	for i, f := range facts {
		sb.WriteString(fmt.Sprintf("%d. [%s] %s (score: %.2f, accessed: %d times)\n",
			i+1, f.Category, f.Content, f.Score, f.AccessCount))
	}

	prompt := compactionPrompt + "\n\n---\n\nCURRENT FACTS (" +
		fmt.Sprintf("%d", len(facts)) + " total):\n\n" + sb.String()

	response, err := sendPrompt(ctx, prompt)
	if err != nil {
		// Fall back to score-based pruning
		c.logger.Warn("LLM compaction failed, falling back to score-based pruning", zap.Error(err))
		return c.RunScoreBased()
	}

	// Parse the LLM response
	consolidated := c.parseCompactionResponse(response, facts)
	if len(consolidated) == 0 {
		c.logger.Warn("LLM returned empty compaction, keeping original facts")
		c.markCompacted()
		return nil
	}

	// Shrink guard: consolidation legitimately merges near-duplicates, but a
	// result that keeps under half the facts is far more likely a truncated
	// model answer than a real cleanup. Refuse it — memory loss is the one
	// failure this subsystem must never have — and curate conservatively.
	if float64(len(consolidated)) < float64(len(facts))*compactionMinKeepRatio {
		c.logger.Warn("LLM compaction kept too few facts — rejecting as truncated output",
			zap.Int("before", len(facts)),
			zap.Int("after", len(consolidated)))
		return c.RunScoreBased()
	}

	// Archive whatever the consolidation dropped BEFORE replacing, so every
	// removed fact stays individually recoverable from memory_archive.json.
	kept := make(map[string]struct{}, len(consolidated))
	for _, f := range consolidated {
		kept[f.ID] = struct{}{}
	}
	var dropped []*Fact
	for _, f := range facts {
		if _, ok := kept[f.ID]; !ok {
			dropped = append(dropped, f)
		}
	}
	if len(dropped) > 0 {
		archivePath := filepath.Join(c.memDir, "memory_archive.json")
		if err := appendFactsToArchive(dropped, archivePath); err != nil {
			c.logger.Warn("failed to archive facts dropped by compaction — keeping original facts",
				zap.Error(err))
			c.markCompacted()
			return nil
		}
	}

	c.logger.Info("Memory compaction complete",
		zap.Int("before", len(facts)),
		zap.Int("after", len(consolidated)),
		zap.Int("archived", len(dropped)))

	c.facts.ReplaceFacts(consolidated)
	c.markCompacted()

	// Regenerate MEMORY.md
	c.writeMemoryMD()

	return nil
}

// RunScoreBased performs score-based compaction (no LLM needed).
func (c *Compactor) RunScoreBased() error {
	c.logger.Info("Starting score-based memory compaction")

	// Archive facts with very low scores
	threshold := 0.1
	candidates := c.facts.GetArchiveCandidates(threshold)

	if len(candidates) > 0 {
		archivePath := filepath.Join(c.memDir, "memory_archive.json")
		if err := c.facts.ArchiveFacts(candidates, archivePath); err != nil {
			c.logger.Warn("Failed to archive facts", zap.Error(err))
		} else {
			c.logger.Info("Archived low-score facts",
				zap.Int("count", len(candidates)))
		}
	}

	c.markCompacted()
	c.writeMemoryMD()

	return nil
}

// CleanupDailyNotes removes old daily notes.
func (c *Compactor) CleanupDailyNotes() (int, error) {
	return c.daily.Cleanup(c.config.DailyNoteRetention)
}

// writeMemoryMD writes MEMORY.md from the current fact index.
func (c *Compactor) writeMemoryMD() {
	content := c.facts.GenerateMarkdown(c.config.MaxMemoryMDSize)
	path := c.memDir + "/MEMORY.md"
	if err := atomicWriteFile(path, []byte(content), 0o600); err != nil {
		c.logger.Warn("Failed to regenerate MEMORY.md", zap.Error(err))
	}
}

// RegenerateMemoryMD is the public version for external callers.
func (c *Compactor) RegenerateMemoryMD() {
	c.writeMemoryMD()
}

// parseCompactionResponse parses the LLM consolidation output back into facts.
func (c *Compactor) parseCompactionResponse(response string, originalFacts []*Fact) []*Fact {
	response = strings.TrimSpace(response)
	lines := strings.Split(response, "\n")

	consolidated := make([]*Fact, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "---") {
			continue
		}

		// Remove bullet/numbering prefix
		line = strings.TrimLeft(line, "0123456789.-) ")
		line = strings.TrimPrefix(line, "* ")
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimSpace(line)

		if line == "" {
			continue
		}

		// Extract category from [category] prefix
		category := "general"
		if strings.HasPrefix(line, "[") {
			end := strings.Index(line, "]")
			if end > 0 {
				category = strings.ToLower(strings.TrimSpace(line[1:end]))
				line = strings.TrimSpace(line[end+1:])
			}
		}

		if line == "" {
			continue
		}

		// Try to find matching original fact to preserve metadata
		var matchedFact *Fact
		for _, of := range originalFacts {
			if strings.Contains(strings.ToLower(of.Content), strings.ToLower(line[:min(50, len(line))])) {
				matchedFact = of
				break
			}
		}

		fact := &Fact{
			Content:      line,
			Category:     category,
			CreatedAt:    time.Now(),
			LastAccessed: time.Now(),
			AccessCount:  1,
			Score:        1.0,
		}

		if matchedFact != nil {
			fact.CreatedAt = matchedFact.CreatedAt
			fact.AccessCount = matchedFact.AccessCount
			fact.Tags = matchedFact.Tags
			// Trust metadata must survive consolidation: without this, every
			// compaction silently downgraded user-stated facts (0.9/1.0) to
			// the extraction default and erased where they were learned.
			fact.Confidence = matchedFact.Confidence
			fact.Provenance = matchedFact.Provenance
			fact.SourceProject = matchedFact.SourceProject
		}

		// Generate ID from content
		fact.ID = c.facts.hashContent(line)

		consolidated = append(consolidated, fact)
	}

	return consolidated
}

const compactionPrompt = `You are a memory consolidation system. Your job is to clean up and optimize a list of remembered facts.

INSTRUCTIONS:
1. Merge duplicate or near-duplicate facts into single, comprehensive entries
2. Remove facts that are clearly obsolete or superseded by newer ones
3. Consolidate related facts about the same topic into concise summaries
4. Preserve the category tag for each fact in [category] format
5. Keep facts that contain unique, actionable information
6. Preserve exact file paths, command names, and technical details
7. Remove trivial or obvious facts that provide no lasting value

OUTPUT FORMAT:
Return ONLY a list of consolidated facts, one per line, with category prefix:
- [category] fact content here
- [category] another fact content

DO NOT add explanations, headers, or commentary. ONLY the fact list.

CATEGORIES: architecture, pattern, preference, gotcha, project, personal, general

RULES:
- If two facts say the same thing differently, keep the more complete one
- If a fact references a specific date and is about a temporary state, remove it
- User preferences and personal info are HIGH priority — always keep
- Technical gotchas and patterns are HIGH priority — always keep
- Write in the same language as the original facts`
