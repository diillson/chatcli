package memory

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Compactor handles LLM-based memory consolidation and cleanup.
type Compactor struct {
	facts  *FactIndex
	daily  *DailyNoteStore
	config Config
	logger *zap.Logger
	memDir string

	lastCompaction time.Time
}

// NewCompactor creates a new memory compactor.
func NewCompactor(facts *FactIndex, daily *DailyNoteStore, config Config, memDir string, logger *zap.Logger) *Compactor {
	return &Compactor{
		facts:  facts,
		daily:  daily,
		config: config,
		logger: logger,
		memDir: memDir,
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
		c.lastCompaction = time.Now()
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
		c.lastCompaction = time.Now()
		return nil
	}

	c.logger.Info("Memory compaction complete",
		zap.Int("before", len(facts)),
		zap.Int("after", len(consolidated)))

	c.facts.ReplaceFacts(consolidated)
	c.lastCompaction = time.Now()

	// Regenerate MEMORY.md
	c.regenerateMemoryMD()

	return nil
}

// RunScoreBased performs score-based compaction (no LLM needed).
func (c *Compactor) RunScoreBased() error {
	c.logger.Info("Starting score-based memory compaction")

	// Archive facts with very low scores
	threshold := 0.1
	candidates := c.facts.GetArchiveCandidates(threshold)

	if len(candidates) > 0 {
		archivePath := c.memDir + "/memory_archive.json"
		if err := c.facts.ArchiveFacts(candidates, archivePath); err != nil {
			c.logger.Warn("Failed to archive facts", zap.Error(err))
		} else {
			c.logger.Info("Archived low-score facts",
				zap.Int("count", len(candidates)))
		}
	}

	c.lastCompaction = time.Now()
	c.regenerateMemoryMD()

	return nil
}

// CleanupDailyNotes removes old daily notes.
func (c *Compactor) CleanupDailyNotes() (int, error) {
	return c.daily.Cleanup(c.config.DailyNoteRetention)
}

// regenerateMemoryMD writes MEMORY.md from the current fact index.
func (c *Compactor) regenerateMemoryMD() {
	content := c.facts.GenerateMarkdown(c.config.MaxMemoryMDSize)
	path := c.memDir + "/MEMORY.md"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		c.logger.Warn("Failed to regenerate MEMORY.md", zap.Error(err))
	}
}

// RegenerateMemoryMD is the public version for external callers.
func (c *Compactor) RegenerateMemoryMD() {
	c.regenerateMemoryMD()
}

// parseCompactionResponse parses the LLM consolidation output back into facts.
func (c *Compactor) parseCompactionResponse(response string, originalFacts []*Fact) []*Fact {
	response = strings.TrimSpace(response)
	lines := strings.Split(response, "\n")

	var consolidated []*Fact
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
