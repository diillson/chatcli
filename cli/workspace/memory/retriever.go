package memory

import (
	"context"
	"fmt"
	"strings"
)

// RelevanceRetriever selects the most relevant memories for the current conversation.
type RelevanceRetriever struct {
	facts        *FactIndex
	profile      *UserProfileStore
	topics       *TopicTracker
	projects     *ProjectTracker
	patterns     *PatternDetector
	daily        *DailyNoteStore
	config       Config
	workspaceDir string // current session workspace for disambiguation
}

// NewRelevanceRetriever creates a new retriever.
func NewRelevanceRetriever(
	facts *FactIndex,
	profile *UserProfileStore,
	topics *TopicTracker,
	projects *ProjectTracker,
	patterns *PatternDetector,
	daily *DailyNoteStore,
	config Config,
) *RelevanceRetriever {
	return &RelevanceRetriever{
		facts:    facts,
		profile:  profile,
		topics:   topics,
		projects: projects,
		patterns: patterns,
		daily:    daily,
		config:   config,
	}
}

// SetWorkspaceDir updates the current workspace directory for disambiguation.
func (r *RelevanceRetriever) SetWorkspaceDir(dir string) {
	r.workspaceDir = dir
}

// RetrieveWithHyDE runs the full HyDE retrieval path — Phase 3a
// (hypothesis-based keyword expansion) and Phase 3b (vector cosine
// search) when both are wired. When augmenter and vectors are nil or
// disabled, the call is byte-identical to a plain Retrieve(hints) —
// the no-regression contract.
//
// query is the raw user question used to seed the hypothesis. Pass an
// empty string to skip augmentation entirely (rare; callers usually
// have at least one user message to feed in).
func (r *RelevanceRetriever) RetrieveWithHyDE(ctx context.Context, query string, hints []string, augmenter *HyDEAugmenter, vectors *VectorIndex) string {
	expanded := hints
	if augmenter != nil {
		expanded = augmenter.Augment(ctx, query, hints)
	}

	// Phase 3b: when a vector index is wired, embed the (augmented)
	// query and union the cosine top-k fact ids with the keyword set.
	// We surface vector hits by promoting them as additional keyword
	// tokens (the fact id), but the truth is FactIndex.Search remains
	// the canonical retriever — vectors only WIDEN the candidate set.
	if vectors != nil && vectors.Enabled() && strings.TrimSpace(query) != "" {
		if vec, err := vectors.EmbedQuery(ctx, query); err == nil {
			// Top-K cosine hits become additional hint tokens via the
			// fact's content. We resolve ids to content here so the
			// existing keyword scorer can re-rank cohesively.
			topIDs := vectors.SimilarFacts(vec, 8)
			if len(topIDs) > 0 {
				for _, id := range topIDs {
					if f, ok := r.facts.GetByID(id); ok {
						expanded = mergeUniqueLowercase(expanded, ExtractKeywords([]string{f.Content}))
					}
				}
			}
		}
	}

	return r.Retrieve(expanded)
}

// Retrieve returns memory context tailored to the current conversation.
// hints are extracted from recent messages (keywords, topics mentioned).
func (r *RelevanceRetriever) Retrieve(hints []string) string {
	budget := r.config.RetrievalBudget
	if budget <= 0 {
		budget = 4000
	}

	var sections []string
	remaining := budget

	// 1. User profile (always included, small)
	if profileText := r.profile.FormatForPrompt(); profileText != "" {
		section := "## User Profile\n\n" + profileText
		if len(section) < remaining {
			sections = append(sections, section)
			remaining -= len(section)
		}
	}

	// 2. Active projects (always included, small)
	if projText := r.projects.FormatForPrompt(); projText != "" {
		section := "## Projects\n\n" + projText
		if len(section) < remaining {
			sections = append(sections, section)
			remaining -= len(section)
		}
	}

	// 3. Top topics (brief)
	if topicText := r.topics.FormatForPrompt(10); topicText != "" {
		section := "## Topics\n\n" + topicText
		if len(section) < remaining {
			sections = append(sections, section)
			remaining -= len(section)
		}
	}

	// 4. Relevant facts — the main section
	var relevantFacts []*Fact
	if len(hints) > 0 {
		relevantFacts = r.facts.Search(hints)
	} else {
		relevantFacts = r.facts.GetAll()
	}

	if len(relevantFacts) > 0 {
		var factLines []string
		var accessedIDs []string
		usedChars := 0
		header := "## Long-term Memory\n\n"
		usedChars += len(header)

		for _, f := range relevantFacts {
			line := fmt.Sprintf("- [%s] %s", f.Category, f.Content)
			// Annotate facts from other projects so the model knows they're not from CWD
			if f.SourceProject != "" && r.workspaceDir != "" && f.SourceProject != r.workspaceDir {
				line += fmt.Sprintf(" (from: %s)", f.SourceProject)
			}
			if usedChars+len(line)+1 > remaining {
				break
			}
			factLines = append(factLines, line)
			accessedIDs = append(accessedIDs, f.ID)
			usedChars += len(line) + 1
		}

		if len(factLines) > 0 {
			section := header + strings.Join(factLines, "\n")
			sections = append(sections, section)
			remaining -= usedChars

			// Mark accessed facts (boost their scores for future)
			r.facts.MarkAccessed(accessedIDs)
		}
	}

	// 5. Recent daily notes (last 3 days, if budget allows)
	recentNotes := r.daily.GetRecentDailyNotes(3)
	if len(recentNotes) > 0 && remaining > 200 {
		var notesParts []string
		for _, note := range recentNotes {
			dateStr := note.Date.Format("2006-01-02")
			noteContent := note.Content
			// Truncate each note if needed
			maxNoteLen := remaining / len(recentNotes)
			if maxNoteLen < 200 {
				maxNoteLen = 200
			}
			if len(noteContent) > maxNoteLen {
				noteContent = noteContent[:maxNoteLen] + "\n...(truncated)"
			}
			notesParts = append(notesParts, fmt.Sprintf("### %s\n\n%s", dateStr, noteContent))
		}
		section := "## Recent Activity\n\n" + strings.Join(notesParts, "\n\n")
		if len(section) <= remaining {
			sections = append(sections, section)
		} else if remaining > 300 {
			// Fit what we can
			section = section[:remaining]
			sections = append(sections, section)
		}
	}

	// 6. Usage patterns (brief, if budget allows)
	if remaining > 100 {
		if patternText := r.patterns.FormatForPrompt(); patternText != "" {
			section := "## Usage Patterns\n\n" + patternText
			if len(section) < remaining {
				sections = append(sections, section)
			}
		}
	}

	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n\n")
}

// RetrieveAll returns the full memory dump (used for /memory longterm).
func (r *RelevanceRetriever) RetrieveAll() string {
	return r.facts.GenerateMarkdown(r.config.MaxMemoryMDSize)
}

// ExtractKeywords extracts keywords from conversation messages for hint-based retrieval.
func ExtractKeywords(messages []string) []string {
	wordFreq := make(map[string]int)

	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true, "was": true,
		"were": true, "be": true, "been": true, "being": true, "have": true,
		"has": true, "had": true, "do": true, "does": true, "did": true,
		"will": true, "would": true, "could": true, "should": true, "may": true,
		"might": true, "can": true, "this": true, "that": true, "these": true,
		"those": true, "i": true, "you": true, "he": true, "she": true,
		"it": true, "we": true, "they": true, "me": true, "him": true,
		"her": true, "us": true, "them": true, "my": true, "your": true,
		"his": true, "its": true, "our": true, "their": true, "what": true,
		"which": true, "who": true, "whom": true, "where": true, "when": true,
		"why": true, "how": true, "not": true, "no": true, "nor": true,
		"but": true, "and": true, "or": true, "so": true, "if": true,
		"then": true, "than": true, "too": true, "very": true, "just": true,
		"don't": true, "about": true, "with": true, "from": true, "into": true,
		"for": true, "on": true, "in": true, "at": true, "to": true,
		"of": true, "by": true, "up": true, "out": true, "as": true,
		// Portuguese stop words
		"os": true, "um": true, "uma": true,
		"de": true, "da": true, "das": true, "dos": true,
		"em": true, "na": true, "nas": true, "nos": true,
		"com": true, "por": true, "para": true, "ou": true,
		"que": true, "se": true, "mas": true, "como": true, "mais": true,
		"qual": true, "quando": true, "onde": true, "quem": true,
		"sim": true, "aqui": true, "ali": true, "isso": true,
		"isto": true, "esse": true, "esta": true, "eu": true,
		"tu": true, "ele": true, "ela": true, "eles": true, //nolint:misspell // "eles" is pt-BR for "they", not a typo
		"voce": true, "meu": true, "seu": true, "sua": true,
	}

	for _, msg := range messages {
		words := strings.Fields(strings.ToLower(msg))
		for _, w := range words {
			// Clean punctuation
			w = strings.Trim(w, ".,;:!?\"'`()[]{}#*-_/\\<>")
			if len(w) < 3 || stopWords[w] {
				continue
			}
			wordFreq[w]++
		}
	}

	// Sort by frequency
	type wf struct {
		word string
		freq int
	}
	var sorted []wf
	for w, f := range wordFreq {
		sorted = append(sorted, wf{w, f})
	}
	if len(sorted) == 0 {
		return nil
	}

	// Sort by frequency descending
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].freq > sorted[i].freq {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	// Return top 20 keywords
	limit := 20
	if len(sorted) < limit {
		limit = len(sorted)
	}
	result := make([]string, limit)
	for i := 0; i < limit; i++ {
		result[i] = sorted[i].word
	}
	return result
}
