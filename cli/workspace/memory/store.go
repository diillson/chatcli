package memory

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
)

// Manager is the central orchestrator for the memory system.
// It owns all sub-stores and provides the same API surface that
// ContextBuilder and memoryWorker currently use, plus new capabilities.
type Manager struct {
	baseDir      string
	workspaceDir string // current session's workspace directory
	logger       *zap.Logger
	config       Config

	Profile  *UserProfileStore
	Facts    *FactIndex
	Topics   *TopicTracker
	Projects *ProjectTracker
	Patterns *PatternDetector
	Daily    *DailyNoteStore

	compactor *Compactor
	retriever *RelevanceRetriever
	migration *Migration
}

// NewManager creates a new memory manager. The memoryDir should be the
// base memory directory (e.g., ~/.chatcli/memory/).
func NewManager(memoryDir string, config Config, logger *zap.Logger) *Manager {
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		logger.Warn("failed to create memory dir", zap.Error(err))
	}

	m := &Manager{
		baseDir: memoryDir,
		logger:  logger,
		config:  config,
	}

	// Initialize all sub-stores
	m.Profile = NewUserProfileStore(memoryDir, logger)
	m.Facts = NewFactIndex(memoryDir, config, logger)
	m.Topics = NewTopicTracker(memoryDir, logger)
	m.Projects = NewProjectTracker(memoryDir, logger)
	m.Patterns = NewPatternDetector(memoryDir, logger)
	m.Daily = NewDailyNoteStore(memoryDir, logger)

	m.compactor = NewCompactor(m.Facts, m.Daily, config, memoryDir, logger)
	m.retriever = NewRelevanceRetriever(m.Facts, m.Profile, m.Topics, m.Projects, m.Patterns, m.Daily, config)
	m.migration = NewMigration(memoryDir, m.Facts, logger)

	// Auto-migrate if needed
	if m.migration.NeedsMigration() {
		if err := m.migration.RunHeuristic(); err != nil {
			logger.Warn("migration failed", zap.Error(err))
		} else {
			m.compactor.RegenerateMemoryMD()
		}
	}

	return m
}

// --- Backward-compatible API (matches old MemoryStore) ---

// GetMemoryContext returns memory context for the system prompt.
// This is the backward-compatible version that dumps relevant context.
func (m *Manager) GetMemoryContext() string {
	return m.retriever.Retrieve(nil)
}

// GetRelevantContext returns memory context tailored to conversation hints.
func (m *Manager) GetRelevantContext(hints []string) string {
	return m.retriever.Retrieve(hints)
}

// ReadLongTerm returns the rendered MEMORY.md content.
func (m *Manager) ReadLongTerm() string {
	return m.Facts.GenerateMarkdown(m.config.MaxMemoryMDSize)
}

// WriteLongTerm replaces all long-term memory with new content.
// This parses the content into facts.
func (m *Manager) WriteLongTerm(content string) error {
	// Parse content into facts
	lines := strings.Split(content, "\n")
	currentCategory := "general"

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			currentCategory = detectCategory(trimmed)
			continue
		}
		factContent := strings.TrimPrefix(trimmed, "- ")
		factContent = strings.TrimPrefix(factContent, "* ")
		factContent = strings.TrimSpace(factContent)
		if len(factContent) >= 5 {
			m.Facts.AddFact(factContent, currentCategory, extractTags(factContent))
		}
	}

	m.compactor.RegenerateMemoryMD()
	return nil
}

// AppendLongTerm adds new content to long-term memory.
func (m *Manager) AppendLongTerm(entry string) error {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return nil
	}

	lines := strings.Split(entry, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "---") {
			continue
		}
		factContent := strings.TrimPrefix(trimmed, "- ")
		factContent = strings.TrimPrefix(factContent, "* ")
		factContent = strings.TrimSpace(factContent)
		if len(factContent) >= 5 {
			m.Facts.AddFactWithSource(factContent, "general", extractTags(factContent), m.workspaceDir)
		}
	}

	m.compactor.RegenerateMemoryMD()
	return nil
}

// WriteDailyNote delegates to the daily note store.
func (m *Manager) WriteDailyNote(entry string) error {
	return m.Daily.WriteDailyNote(entry)
}

// GetRecentDailyNotes delegates to the daily note store.
func (m *Manager) GetRecentDailyNotes(days int) []DailyNote {
	return m.Daily.GetRecentDailyNotes(days)
}

// TodayNotePath delegates to the daily note store.
func (m *Manager) TodayNotePath() string {
	return m.Daily.TodayNotePath()
}

// EnsureDirectories creates the memory directory structure.
func (m *Manager) EnsureDirectories() error {
	return os.MkdirAll(m.baseDir, 0o755)
}

// --- Enhanced API ---

// ProcessExtraction processes the output from the memory extraction LLM.
// This replaces the old AppendLongTerm + WriteDailyNote pattern with
// structured extraction that populates profile, topics, and projects.
func (m *Manager) ProcessExtraction(response string) {
	daily, longTerm, profileUpdates, topics, projects := parseEnhancedResponse(response)

	if daily != "" {
		if err := m.Daily.WriteDailyNote(daily); err != nil {
			m.logger.Warn("failed to write daily note", zap.Error(err))
		}
	}

	if longTerm != "" {
		_ = m.AppendLongTerm(longTerm)
	}

	if len(profileUpdates) > 0 {
		m.Profile.Update(profileUpdates)
	}

	if len(topics) > 0 {
		m.Topics.Record(topics)
	}

	if len(projects) > 0 {
		m.Projects.Upsert(projects)
	}
}

// RecordInteraction records a usage event.
func (m *Manager) RecordInteraction(event InteractionEvent) {
	m.Patterns.RecordInteraction(event)

	if event.Command != "" {
		m.Profile.RecordCommand(event.Command)
	}
}

// RunCompaction runs memory compaction (LLM-assisted or score-based).
func (m *Manager) RunCompaction(ctx context.Context, sendPrompt func(ctx context.Context, prompt string) (string, error)) error {
	if sendPrompt != nil {
		return m.compactor.RunWithLLM(ctx, sendPrompt)
	}
	return m.compactor.RunScoreBased()
}

// NeedsCompaction checks if compaction should run.
func (m *Manager) NeedsCompaction() bool {
	return m.compactor.NeedsCompaction()
}

// CleanupDailyNotes removes old daily notes.
func (m *Manager) CleanupDailyNotes() (int, error) {
	return m.compactor.CleanupDailyNotes()
}

// GetConfig returns the current config.
func (m *Manager) GetConfig() Config {
	return m.config
}

// Stats returns a summary of memory system state.
func (m *Manager) Stats() map[string]interface{} {
	return map[string]interface{}{
		"facts_count":    m.Facts.Count(),
		"topics_count":   len(m.Topics.GetAll()),
		"projects_count": len(m.Projects.GetAll()),
		"profile":        m.Profile.Get(),
		"usage_stats":    m.Patterns.GetStats(),
	}
}

// --- Response Parsing ---

// parseEnhancedResponse parses the enhanced extraction prompt response.
func parseEnhancedResponse(response string) (daily, longTerm string, profile map[string]string, topics []string, projects map[string]string) {
	profile = make(map[string]string)
	projects = make(map[string]string)

	upper := strings.ToUpper(response)

	// Find all section positions
	type section struct {
		name string
		idx  int
	}
	sections := []section{
		{"DAILY", findSection(upper, "DAILY")},
		{"LONGTERM", findSection(upper, "LONGTERM")},
		{"PROFILE_UPDATE", findSection(upper, "PROFILE_UPDATE")},
		{"PROFILE", findSection(upper, "PROFILE")},
		{"TOPICS", findSection(upper, "TOPICS")},
		{"PROJECTS", findSection(upper, "PROJECTS")},
	}

	// Filter found sections and sort by position
	var found []section
	for _, s := range sections {
		if s.idx >= 0 {
			found = append(found, s)
		}
	}
	for i := 0; i < len(found); i++ {
		for j := i + 1; j < len(found); j++ {
			if found[j].idx < found[i].idx {
				found[i], found[j] = found[j], found[i]
			}
		}
	}

	// Extract content between sections
	extractContent := func(startIdx int, nextIdx int) string {
		// Find end of header line
		nlIdx := strings.Index(response[startIdx:], "\n")
		if nlIdx < 0 {
			return ""
		}
		contentStart := startIdx + nlIdx + 1
		contentEnd := len(response)
		if nextIdx > 0 {
			contentEnd = nextIdx
		}
		if contentStart >= contentEnd {
			return ""
		}
		return strings.TrimSpace(response[contentStart:contentEnd])
	}

	for i, sec := range found {
		nextIdx := -1
		if i+1 < len(found) {
			nextIdx = found[i+1].idx
		}
		content := extractContent(sec.idx, nextIdx)

		if isNothingNew(content) {
			continue
		}

		switch sec.name {
		case "DAILY":
			daily = content
		case "LONGTERM":
			longTerm = content
		case "PROFILE_UPDATE", "PROFILE":
			profile = parseKeyValues(content)
		case "TOPICS":
			topics = parseCSV(content)
		case "PROJECTS":
			projects = parseKeyValues(content)
		}
	}

	// If no sections found, treat as daily
	if len(found) == 0 && !isNothingNew(response) {
		daily = response
	}

	return
}

func findSection(upper string, keyword string) int {
	patterns := []string{
		"## " + keyword,
		"##" + keyword,
		"# " + keyword,
		"**" + keyword + "**",
	}
	best := -1
	for _, p := range patterns {
		idx := strings.Index(upper, p)
		if idx >= 0 && (best < 0 || idx < best) {
			best = idx
		}
	}
	// Also try LONG-TERM and LONG_TERM variants for LONGTERM
	if keyword == "LONGTERM" {
		for _, alt := range []string{"LONG-TERM", "LONG_TERM"} {
			for _, p := range []string{"## " + alt, "##" + alt, "# " + alt} {
				idx := strings.Index(upper, p)
				if idx >= 0 && (best < 0 || idx < best) {
					best = idx
				}
			}
		}
	}
	return best
}

func isNothingNew(s string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(s))
	normalized = strings.TrimRight(normalized, ".!,;:")
	normalized = strings.TrimSpace(normalized)
	switch normalized {
	case "NOTHING_NEW", "NOTHING NEW", "NOTHING-NEW", "N/A", "NONE", "NA", "":
		return true
	}
	return false
}

func parseKeyValues(content string) map[string]string {
	result := make(map[string]string)
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "* ")

		// Support KEY=VALUE and KEY: VALUE formats
		var key, value string
		if idx := strings.Index(line, "="); idx > 0 {
			key = strings.TrimSpace(line[:idx])
			value = strings.TrimSpace(line[idx+1:])
		} else if idx := strings.Index(line, ":"); idx > 0 {
			key = strings.TrimSpace(line[:idx])
			value = strings.TrimSpace(line[idx+1:])
		}

		if key != "" && value != "" {
			result[key] = value
		}
	}
	return result
}

func parseCSV(content string) []string {
	// Handle both comma-separated and line-separated
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	var items []string

	// First try comma-separated on a single line
	if !strings.Contains(content, "\n") || strings.Contains(content, ",") {
		parts := strings.Split(content, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			p = strings.TrimPrefix(p, "- ")
			p = strings.TrimPrefix(p, "* ")
			p = strings.TrimSpace(p)
			if p != "" {
				items = append(items, p)
			}
		}
	}

	// Also check line-separated
	if len(items) == 0 {
		lines := strings.Split(content, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			line = strings.TrimPrefix(line, "- ")
			line = strings.TrimPrefix(line, "* ")
			line = strings.TrimSpace(line)
			if line != "" {
				items = append(items, line)
			}
		}
	}

	return items
}

// EnhancedExtractionPrompt is the updated prompt for the memory worker.
const EnhancedExtractionPrompt = `You are a memory annotation system. Analyze this conversation segment and extract structured annotations.

OUTPUT FORMAT — use EXACTLY these section headers:

## DAILY
Write a brief log of what was done in this segment. Use bullet points. Include:
- Files read, modified or created (with paths)
- Commands executed and their outcomes
- Errors encountered and how they were resolved
- Tasks completed or in progress

## LONGTERM
Write ONLY genuinely new facts that should be remembered permanently:
- Architectural decisions made
- Patterns or conventions discovered/established
- User preferences expressed
- Important file paths or project structure insights
- Technical constraints or gotchas learned

## PROFILE_UPDATE
If the user revealed new information about themselves, output KEY=VALUE pairs:
name=...
role=...
expertise_level=beginner|intermediate|expert
preferred_language=...
communication_style=...
(Only include fields that have new information. Skip this section if nothing new.)

## TOPICS
List technical topics discussed in this segment (comma-separated):
Go, Bubble Tea, memory systems, ...
(Skip if no clear technical topics.)

## PROJECTS
If a specific project was worked on, output KEY=VALUE pairs:
project_name=...
project_path=...
project_status=active|paused|completed
project_description=...
project_technologies=Go, React, ...
(Skip if no project context.)

RULES:
- If nothing new was learned for a section, write "NOTHING_NEW" in that section
- If the conversation is trivial (greetings, simple questions), respond with just: NOTHING_NEW
- Keep each bullet to ONE line
- Use exact file paths, never paraphrase
- Do NOT repeat facts already in EXISTING LONG-TERM MEMORY
- Write in the same language the user is using in the conversation
- Be concise — this is metadata, not prose`

// FormatExistingContext builds the context section for the extraction prompt,
// including existing memory to avoid duplication.
func (m *Manager) FormatExistingContext() string {
	var parts []string

	longTerm := m.ReadLongTerm()
	if longTerm != "" {
		parts = append(parts, "EXISTING LONG-TERM MEMORY (do NOT duplicate these facts):\n\n"+longTerm)
	}

	profile := m.Profile.FormatForPrompt()
	if profile != "" {
		parts = append(parts, "EXISTING USER PROFILE:\n\n"+profile)
	}

	topics := m.Topics.FormatForPrompt(15)
	if topics != "" {
		parts = append(parts, "KNOWN TOPICS:\n\n"+topics)
	}

	projects := m.Projects.FormatForPrompt()
	if projects != "" {
		parts = append(parts, "KNOWN PROJECTS:\n\n"+projects)
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// MemoryDir returns the base memory directory path.
func (m *Manager) MemoryDir() string {
	return m.baseDir
}

// SetWorkspaceDir sets the current session's workspace directory.
// Facts created during this session will be annotated with this path.
// The retriever also uses this to disambiguate facts from other projects.
func (m *Manager) SetWorkspaceDir(dir string) {
	m.workspaceDir = dir
	m.retriever.SetWorkspaceDir(dir)
}

// WorkspaceDir returns the current session's workspace directory.
func (m *Manager) WorkspaceDir() string {
	return m.workspaceDir
}

// FormatStats returns a formatted string of memory system statistics.
func (m *Manager) FormatStats() string {
	stats := m.Stats()
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Facts: %d\n", stats["facts_count"]))
	sb.WriteString(fmt.Sprintf("Topics: %d\n", stats["topics_count"]))
	sb.WriteString(fmt.Sprintf("Projects: %d\n", stats["projects_count"]))

	usageStats, _ := stats["usage_stats"].(UsageStats)
	if usageStats.SessionCount > 0 {
		sb.WriteString(fmt.Sprintf("Sessions: %d\n", usageStats.SessionCount))
		sb.WriteString(fmt.Sprintf("Total messages: %d\n", usageStats.TotalMessages))
	}

	return sb.String()
}
