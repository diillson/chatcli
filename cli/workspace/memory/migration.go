package memory

import (
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Migration handles one-time migration from flat MEMORY.md to structured format.
type Migration struct {
	memDir string
	facts  *FactIndex
	logger *zap.Logger
}

// NewMigration creates a new migration helper.
func NewMigration(memDir string, facts *FactIndex, logger *zap.Logger) *Migration {
	return &Migration{memDir: memDir, facts: facts, logger: logger}
}

// NeedsMigration checks if legacy MEMORY.md exists but no memory_index.json.
func (m *Migration) NeedsMigration() bool {
	memPath := m.memDir + "/MEMORY.md"
	indexPath := m.memDir + "/memory_index.json"

	_, memErr := os.Stat(memPath)
	_, idxErr := os.Stat(indexPath)

	return memErr == nil && os.IsNotExist(idxErr)
}

// RunHeuristic performs a heuristic migration (no LLM needed).
// Splits MEMORY.md by lines and creates one fact per meaningful line.
func (m *Migration) RunHeuristic() error {
	memPath := m.memDir + "/MEMORY.md"
	data, err := os.ReadFile(memPath) //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
	if err != nil {
		return err
	}

	content := string(data)
	if strings.TrimSpace(content) == "" {
		return nil
	}

	m.logger.Info("Migrating legacy MEMORY.md to structured format")

	lines := strings.Split(content, "\n")
	currentCategory := "general"
	migrated := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Detect category from markdown headers
		if strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") {
			currentCategory = detectCategory(trimmed)
			continue
		}

		// Skip non-content lines
		if strings.HasPrefix(trimmed, "---") || strings.HasPrefix(trimmed, "===") {
			continue
		}

		// Remove bullet point prefix
		factContent := trimmed
		factContent = strings.TrimPrefix(factContent, "- ")
		factContent = strings.TrimPrefix(factContent, "* ")
		factContent = strings.TrimPrefix(factContent, "+ ")
		factContent = strings.TrimLeft(factContent, "0123456789.) ")
		factContent = strings.TrimSpace(factContent)

		if len(factContent) < 5 {
			continue
		}

		tags := extractTags(factContent)
		if m.facts.AddFact(factContent, currentCategory, tags) {
			migrated++
		}
	}

	m.logger.Info("Migration complete",
		zap.Int("facts_migrated", migrated),
		zap.Int("total_lines", len(lines)))

	// Backup original
	backupPath := m.memDir + "/MEMORY.md.bak"
	if err := os.WriteFile(backupPath, data, 0o600); err != nil { //#nosec G703 -- path validated by engine.validatePath / SensitiveReadPaths.IsReadAllowed
		m.logger.Warn("Failed to create MEMORY.md backup", zap.Error(err))
	}

	return nil
}

// RunWithLLM performs LLM-assisted migration for better categorization.
func (m *Migration) RunWithLLM(sendPrompt func(prompt string) (string, error)) error {
	memPath := m.memDir + "/MEMORY.md"
	data, err := os.ReadFile(memPath) //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
	if err != nil {
		return err
	}

	content := string(data)
	if strings.TrimSpace(content) == "" {
		return nil
	}

	prompt := migrationPrompt + "\n\n---\n\nMEMORY.md CONTENT:\n\n" + content

	response, err := sendPrompt(prompt)
	if err != nil {
		m.logger.Warn("LLM migration failed, falling back to heuristic", zap.Error(err))
		return m.RunHeuristic()
	}

	// Parse structured response
	lines := strings.Split(response, "\n")
	migrated := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "---") {
			continue
		}

		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "* ")
		line = strings.TrimSpace(line)

		category := "general"
		if strings.HasPrefix(line, "[") {
			end := strings.Index(line, "]")
			if end > 0 {
				category = strings.ToLower(strings.TrimSpace(line[1:end]))
				line = strings.TrimSpace(line[end+1:])
			}
		}

		if len(line) < 5 {
			continue
		}

		tags := extractTags(line)
		if m.facts.AddFact(line, category, tags) {
			migrated++
		}
	}

	m.logger.Info("LLM migration complete", zap.Int("facts_migrated", migrated))

	// Backup original
	backupPath := m.memDir + "/MEMORY.md.bak." + time.Now().Format("20060102150405")
	_ = os.WriteFile(backupPath, data, 0o600) //#nosec G703 -- path validated by engine.validatePath / SensitiveReadPaths.IsReadAllowed

	return nil
}

// --- helpers ---

func detectCategory(header string) string {
	lower := strings.ToLower(header)
	lower = strings.TrimLeft(lower, "# ")

	switch {
	case strings.Contains(lower, "architecture") || strings.Contains(lower, "arquitetura"):
		return "architecture"
	case strings.Contains(lower, "pattern") || strings.Contains(lower, "padr"):
		return "pattern"
	case strings.Contains(lower, "preference") || strings.Contains(lower, "preferenc"):
		return "preference"
	case strings.Contains(lower, "gotcha") || strings.Contains(lower, "caveat") || strings.Contains(lower, "pitfall"):
		return "gotcha"
	case strings.Contains(lower, "project") || strings.Contains(lower, "projeto"):
		return "project"
	case strings.Contains(lower, "personal") || strings.Contains(lower, "user") || strings.Contains(lower, "pessoal"):
		return "personal"
	case strings.Contains(lower, "error") || strings.Contains(lower, "bug") || strings.Contains(lower, "fix"):
		return "gotcha"
	case strings.Contains(lower, "key") || strings.Contains(lower, "fact") || strings.Contains(lower, "memory"):
		return "general"
	default:
		return "general"
	}
}

func extractTags(content string) []string {
	var tags []string
	lower := strings.ToLower(content)

	// Simple keyword-based tag extraction
	techKeywords := map[string]string{
		"go ":        "go",
		"golang":     "go",
		"python":     "python",
		"react":      "react",
		"docker":     "docker",
		"k8s":        "kubernetes",
		"kubernetes": "kubernetes",
		"aws":        "aws",
		"gcp":        "gcp",
		"sql":        "sql",
		"api":        "api",
		"rest":       "api",
		"grpc":       "grpc",
		"git":        "git",
		"ci/cd":      "cicd",
		"test":       "testing",
		"bubble tea": "bubbletea",
		"bubbletea":  "bubbletea",
		"tui":        "tui",
		"cli":        "cli",
		"oauth":      "auth",
		"auth":       "auth",
		"memory":     "memory",
		"plugin":     "plugin",
		"llm":        "llm",
		"openai":     "openai",
		"claude":     "claude",
		"gemini":     "gemini",
	}

	seen := make(map[string]bool)
	for keyword, tag := range techKeywords {
		if strings.Contains(lower, keyword) && !seen[tag] {
			tags = append(tags, tag)
			seen[tag] = true
		}
	}

	return tags
}

const migrationPrompt = `You are migrating a flat MEMORY.md file into a structured fact database.

For each meaningful piece of information in the file, output ONE line in this format:
- [category] fact content here

CATEGORIES:
- architecture: Architectural decisions, system design, module structure
- pattern: Code patterns, conventions, best practices
- preference: User preferences, communication style, language
- gotcha: Bugs, pitfalls, known issues, workarounds
- project: Project details, paths, technologies, goals
- personal: User personal info (name, role, expertise)
- general: Anything that doesn't fit above

RULES:
- One fact per line
- Merge related bullet points into single comprehensive facts
- Preserve exact file paths, commands, and technical details
- Remove trivial or redundant entries
- Keep the same language as the original content
- DO NOT add commentary — ONLY output the fact list`
