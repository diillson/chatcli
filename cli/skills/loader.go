package skills

import (
	"bufio"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// SkillsLoader discovers and manages skills.
type SkillsLoader struct {
	workspaceDir string // ./skills/
	globalDir    string // ~/.chatcli/skills/
	skills       map[string]*Skill
	mu           sync.RWMutex
	logger       *zap.Logger
}

// NewSkillsLoader creates a new skills loader.
func NewSkillsLoader(workspaceDir, globalDir string, logger *zap.Logger) *SkillsLoader {
	return &SkillsLoader{
		workspaceDir: workspaceDir,
		globalDir:    globalDir,
		skills:       make(map[string]*Skill),
		logger:       logger,
	}
}

// Discover scans skill directories and loads metadata (NOT full content).
func (sl *SkillsLoader) Discover() error {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	discovered := make(map[string]*Skill)

	// Load global skills first (lower priority)
	if sl.globalDir != "" {
		sl.scanDirectory(sl.globalDir, SourceGlobal, discovered)
	}

	// Load workspace skills (higher priority, overwrites global)
	if sl.workspaceDir != "" {
		sl.scanDirectory(sl.workspaceDir, SourceWorkspace, discovered)
	}

	sl.skills = discovered
	sl.logger.Debug("skills discovered", zap.Int("count", len(discovered)))
	return nil
}

func (sl *SkillsLoader) scanDirectory(dir string, source SkillSource, discovered map[string]*Skill) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			sl.logger.Debug("failed to scan skills directory", zap.String("dir", dir), zap.Error(err))
		}
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillPath := filepath.Join(dir, entry.Name(), "SKILL.md")
		skill, err := parseFrontmatter(skillPath)
		if err != nil {
			// No SKILL.md or parse error; use directory name
			skill = &Skill{
				Name: entry.Name(),
				Path: skillPath,
			}
			// Check if SKILL.md exists at all
			if _, statErr := os.Stat(skillPath); statErr != nil {
				continue
			}
		}

		if skill.Name == "" {
			skill.Name = entry.Name()
		}
		skill.Path = skillPath
		skill.Source = source
		skill.LoadedAt = time.Now()

		discovered[skill.Name] = skill
	}
}

// GetSkill returns a skill by name.
func (sl *SkillsLoader) GetSkill(name string) (*Skill, bool) {
	sl.mu.RLock()
	defer sl.mu.RUnlock()
	s, ok := sl.skills[name]
	return s, ok
}

// ListSkills returns all discovered skills sorted by name.
func (sl *SkillsLoader) ListSkills() []*Skill {
	sl.mu.RLock()
	defer sl.mu.RUnlock()
	result := make([]*Skill, 0, len(sl.skills))
	for _, s := range sl.skills {
		result = append(result, s)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// LoadSkillContent reads the full SKILL.md content (lazy loading).
func (sl *SkillsLoader) LoadSkillContent(name string) (string, error) {
	sl.mu.RLock()
	skill, ok := sl.skills[name]
	sl.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("skill %q not found", name)
	}

	data, err := os.ReadFile(skill.Path)
	if err != nil {
		return "", fmt.Errorf("reading skill %q: %w", name, err)
	}
	return string(data), nil
}

// BuildSkillsSummary generates an XML summary for the system prompt.
func (sl *SkillsLoader) BuildSkillsSummary() string {
	skills := sl.ListSkills()
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<skills>\n")
	for _, s := range skills {
		sb.WriteString("  <skill>\n")
		sb.WriteString(fmt.Sprintf("    <name>%s</name>\n", xmlEscape(s.Name)))
		sb.WriteString(fmt.Sprintf("    <description>%s</description>\n", xmlEscape(s.Description)))
		sb.WriteString(fmt.Sprintf("    <location>%s</location>\n", xmlEscape(s.Path)))
		sb.WriteString("  </skill>\n")
	}
	sb.WriteString("</skills>\n")
	sb.WriteString("\nTo use a skill, read its SKILL.md file using the read tool.\n")
	return sb.String()
}

// RegisterSkill manually registers a skill.
func (sl *SkillsLoader) RegisterSkill(skill *Skill) {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	sl.skills[skill.Name] = skill
}

// UnregisterSkill removes a skill.
func (sl *SkillsLoader) UnregisterSkill(name string) {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	delete(sl.skills, name)
}

// parseFrontmatter extracts YAML frontmatter from a SKILL.md file.
func parseFrontmatter(path string) (*Skill, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var inFrontmatter bool
	var frontmatterLines []string

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if !inFrontmatter {
			if trimmed == "---" {
				inFrontmatter = true
				continue
			}
			// No frontmatter; use first line as description
			skill := &Skill{Description: trimmed}
			return skill, nil
		}

		if trimmed == "---" {
			// End of frontmatter
			break
		}
		frontmatterLines = append(frontmatterLines, line)
	}

	if len(frontmatterLines) == 0 {
		return &Skill{}, nil
	}

	frontmatter := strings.Join(frontmatterLines, "\n")
	var skill Skill
	if err := yaml.Unmarshal([]byte(frontmatter), &skill); err != nil {
		return nil, fmt.Errorf("parsing frontmatter: %w", err)
	}
	return &skill, nil
}

func xmlEscape(s string) string {
	return html.EscapeString(s)
}
