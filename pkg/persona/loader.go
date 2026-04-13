/*
 * ChatCLI - Persona System
 * pkg/persona/loader.go
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package persona

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// Loader handles loading agents and skills from disk
type Loader struct {
	logger     *zap.Logger
	agentsDir  string
	skillsDir  string
	projectDir string // Optional project-local directory for context-aware skills
}

// NewLoader creates a new persona loader with default paths
func NewLoader(logger *zap.Logger) *Loader {
	home, err := os.UserHomeDir()
	if err != nil {
		logger.Error("Failed to get user home directory", zap.Error(err))
		// Fallback to current directory to avoid panic, though behavior might be degraded
		home = "."
	}
	baseDir := filepath.Join(home, ".chatcli")

	return &Loader{
		logger:    logger,
		agentsDir: filepath.Join(baseDir, "agents"),
		skillsDir: filepath.Join(baseDir, "skills"),
	}
}

// SetProjectDir sets an optional project-local directory for skills lookup
// Priority: Project Local > Global
func (l *Loader) SetProjectDir(dir string) {
	if dir != "" {
		absDir, err := filepath.Abs(dir)
		if err == nil {
			l.projectDir = absDir
		} else {
			l.projectDir = dir
		}
	}
}

// ListAgents returns all available agents scanning both project-local and global directories.
// Priority: Project Local (.agent/agents/) > Global (~/.chatcli/agents/)
func (l *Loader) ListAgents() ([]*Agent, error) {
	var agents []*Agent
	seen := make(map[string]bool)

	scanDir := func(basePath string) {
		if _, err := os.Stat(basePath); os.IsNotExist(err) {
			return
		}

		entries, err := os.ReadDir(basePath)
		if err != nil {
			l.logger.Warn("Failed to read agents directory", zap.String("path", basePath), zap.Error(err))
			return
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}

			path := filepath.Join(basePath, entry.Name())
			agent, err := l.loadAgentFile(path)
			if err != nil {
				l.logger.Warn("Failed to load agent file, skipping",
					zap.String("path", path),
					zap.Error(err))
				continue
			}

			// Skip if already loaded (Project overrides Global)
			if seen[agent.Name] {
				continue
			}

			agents = append(agents, agent)
			seen[agent.Name] = true
		}
	}

	// 1. Load from project directory first (higher priority)
	if l.projectDir != "" {
		projectAgentsDir := filepath.Join(l.projectDir, ".agent", "agents")
		scanDir(projectAgentsDir)
	}

	// 2. Load from global directory
	scanDir(l.agentsDir)

	return agents, nil
}

// ListSkills returns all available skills (names only for listing purposes).
// It performs a shallow scan of both project-local and global directories.
//
// When multiple skills share the same base name (e.g. a local "frontend-design"
// and a skills.sh "anthropics-skills--frontend-design"), dedup uses the skill's
// frontmatter name — NOT the directory name — so only one instance is returned.
// User preferences from skill-preferences.yaml are honored: if the user prefers
// the skills.sh version, that one wins even if a local version exists.
func (l *Loader) ListSkills() ([]*Skill, error) {
	var skills []*Skill
	// seen tracks by frontmatter name (e.g. "frontend-design"), not directory name.
	// This prevents loading duplicate skills from different sources.
	seen := make(map[string]bool)

	// Load user preferences to resolve conflicts
	prefs := l.loadSkillPreferences()

	// deferred holds skills that were skipped because a same-name skill was
	// already loaded, but the user might prefer this one instead.
	type deferredSkill struct {
		skill  *Skill
		source string // source from frontmatter (e.g. "skills.sh", "local")
	}
	var deferred []deferredSkill

	// Helper to scan a directory
	scanDir := func(basePath string) {
		if _, err := os.Stat(basePath); os.IsNotExist(err) {
			return
		}

		entries, err := os.ReadDir(basePath)
		if err != nil {
			l.logger.Warn("Failed to read skills directory", zap.String("path", basePath), zap.Error(err))
			return
		}

		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			fullPath := filepath.Join(basePath, entry.Name())

			var skill *Skill
			var loadErr error

			if entry.IsDir() {
				skill, loadErr = l.loadSkillFromPackage(fullPath)
			} else if strings.HasSuffix(entry.Name(), ".md") {
				skill, loadErr = l.loadSkillFile(fullPath)
			}

			if loadErr != nil {
				l.logger.Debug("Skipping entry in skills dir", zap.String("entry", entry.Name()), zap.Error(loadErr))
				continue
			}
			if skill == nil || skill.Name == "" {
				continue
			}

			if seen[skill.Name] {
				// Same base name already loaded — defer for preference check
				source := l.extractSourceFromFrontmatter(fullPath)
				if source == "" {
					source = "local"
				}
				deferred = append(deferred, deferredSkill{skill: skill, source: source})
				continue
			}

			skills = append(skills, skill)
			seen[skill.Name] = true
		}
	}

	// 1. Load from project directory first (higher priority)
	if l.projectDir != "" {
		projectSkillsDir := filepath.Join(l.projectDir, ".agent", "skills")
		scanDir(projectSkillsDir)
	}

	// 2. Load from global directory
	scanDir(l.skillsDir)

	// 3. Apply user preferences: if a deferred skill matches a preference,
	// swap it in place of the default-priority version.
	if len(prefs) > 0 && len(deferred) > 0 {
		for _, d := range deferred {
			preferred := prefs[d.skill.Name]
			if preferred == "" || preferred != d.source {
				continue
			}
			// User prefers this deferred version — swap it in
			for i, s := range skills {
				if s.Name == d.skill.Name {
					skills[i] = d.skill
					l.logger.Debug("skill preference applied",
						zap.String("name", d.skill.Name),
						zap.String("preferred_source", d.source))
					break
				}
			}
		}
	}

	return skills, nil
}

// GetAgent returns an agent by name.
// It prioritizes:
// 1. Project-local agent file (.agent/agents/{name}.md)
// 2. Project-local agent by metadata search
// 3. Global agent file (~/.chatcli/agents/{name}.md)
// 4. Global agent by metadata search
func (l *Loader) GetAgent(name string) (*Agent, error) {
	checkLocation := func(basePath string) (*Agent, error) {
		// Try exact filename first
		path := filepath.Join(basePath, name+".md")
		if _, err := os.Stat(path); err == nil {
			return l.loadAgentFile(path)
		}

		// Fallback: scan directory for matching metadata name
		if _, err := os.Stat(basePath); os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		entries, err := os.ReadDir(basePath)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			agent, err := l.loadAgentFile(filepath.Join(basePath, entry.Name()))
			if err != nil {
				continue
			}
			if strings.EqualFold(agent.Name, name) {
				return agent, nil
			}
		}
		return nil, os.ErrNotExist
	}

	// 1. Check project directory
	if l.projectDir != "" {
		projectAgentsDir := filepath.Join(l.projectDir, ".agent", "agents")
		if agent, err := checkLocation(projectAgentsDir); err == nil {
			return agent, nil
		}
	}

	// 2. Check global directory
	if agent, err := checkLocation(l.agentsDir); err == nil {
		return agent, nil
	}

	return nil, fmt.Errorf("agent not found: %s (checked local and global)", name)
}

// qualifiedSep is the separator between source prefix and base name in
// qualified skill directory names. Mirrors registry.qualifiedSeparator.
const qualifiedSep = "--"

// GetSkill locates and loads a skill by name.
// It prioritizes:
//  1. Project-local Package (Folder with SKILL.md) — exact name
//  2. Project-local File (.md) — exact name
//  3. Global Package — exact name
//  4. Global File — exact name
//  5. Fallback: search by base name across qualified directories
//     (e.g. "frontend-design" matches "anthropics-skills--frontend-design")
func (l *Loader) GetSkill(name string) (*Skill, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("skill name cannot be empty")
	}

	// Helper to check package vs file by exact directory/file name
	checkLocation := func(basePath string) (*Skill, error) {
		// Check Package (Folder)
		packagePath := filepath.Join(basePath, name)
		info, err := os.Stat(packagePath)
		if err == nil && info.IsDir() {
			// Validate if it's a valid skill package
			if _, err := os.Stat(filepath.Join(packagePath, "SKILL.md")); err == nil {
				return l.loadSkillFromPackage(packagePath)
			}
		}

		// Check Single File
		filePath := filepath.Join(basePath, name+".md")
		if _, err := os.Stat(filePath); err == nil {
			return l.loadSkillFile(filePath)
		}

		return nil, os.ErrNotExist
	}

	// Helper to search for qualified names matching a base name.
	// Scans directory entries for "{prefix}--{name}" patterns.
	// When multiple matches exist, checks user preferences to pick the right one.
	// Falls back to first match found if no preference is set.
	findByBaseName := func(basePath string) (*Skill, error) {
		entries, err := os.ReadDir(basePath)
		if err != nil {
			return nil, os.ErrNotExist
		}

		// Collect all qualified matches
		type candidate struct {
			dirName string
			path    string
		}
		var candidates []candidate

		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			dirName := entry.Name()
			sepIdx := strings.LastIndex(dirName, qualifiedSep)
			if sepIdx <= 0 {
				continue
			}
			basePart := dirName[sepIdx+len(qualifiedSep):]
			if basePart == name {
				packagePath := filepath.Join(basePath, dirName)
				if _, err := os.Stat(filepath.Join(packagePath, "SKILL.md")); err == nil {
					candidates = append(candidates, candidate{dirName: dirName, path: packagePath})
				}
			}
		}

		if len(candidates) == 0 {
			return nil, os.ErrNotExist
		}

		// If only one match, use it directly
		if len(candidates) == 1 {
			return l.loadSkillFromPackage(candidates[0].path)
		}

		// Multiple matches — check user preference
		prefs := l.loadSkillPreferences()
		preferred := prefs[name]
		if preferred != "" {
			for _, c := range candidates {
				// Load to check source field
				skill, err := l.loadSkillFromPackage(c.path)
				if err == nil && skill != nil {
					source := l.extractSourceFromFrontmatter(c.path)
					if source == preferred {
						return skill, nil
					}
				}
			}
		}

		// No preference or preference didn't match — use first candidate
		return l.loadSkillFromPackage(candidates[0].path)
	}

	// 0. Check if user has a preference for this skill name.
	// If so, try to honor it FIRST — overriding the default priority order.
	prefs := l.loadSkillPreferences()
	preferredSource := prefs[name]

	if preferredSource != "" && preferredSource != "local" {
		// User prefers a registry version — try qualified dirs first
		dirsToSearch := []string{l.skillsDir}
		if l.projectDir != "" {
			dirsToSearch = append([]string{filepath.Join(l.projectDir, ".agent", "skills")}, dirsToSearch...)
		}
		for _, dir := range dirsToSearch {
			if skill, err := findByBaseName(dir); err == nil {
				return skill, nil
			}
		}
	}

	// 1. Check Project Directory (exact name)
	if l.projectDir != "" {
		projectSkillsDir := filepath.Join(l.projectDir, ".agent", "skills")
		if skill, err := checkLocation(projectSkillsDir); err == nil {
			return skill, nil
		}
	}

	// 2. Check Global Directory (exact name)
	if skill, err := checkLocation(l.skillsDir); err == nil {
		return skill, nil
	}

	// 3. Fallback: search by base name in qualified directories.
	// Project-local first, then global — so local skills always win.
	if l.projectDir != "" {
		projectSkillsDir := filepath.Join(l.projectDir, ".agent", "skills")
		if skill, err := findByBaseName(projectSkillsDir); err == nil {
			return skill, nil
		}
	}
	if skill, err := findByBaseName(l.skillsDir); err == nil {
		return skill, nil
	}

	return nil, fmt.Errorf("skill not found: '%s' (checked local and global)", name)
}

// loadSkillFromPackage loads a V2 skill structure (Directory based)
// loadSkillPreferences reads the skill preferences file and returns
// a map of base-name → preferred-source. Cached per call (no file watch).
func (l *Loader) loadSkillPreferences() map[string]string {
	prefsPath := filepath.Clean(filepath.Join(filepath.Dir(l.skillsDir), "skill-preferences.yaml"))

	data, err := os.ReadFile(prefsPath) // #nosec G304 -- path derived from skillsDir (user home)
	if err != nil {
		return nil
	}

	var prefs struct {
		Preferences map[string]string `yaml:"preferences"`
	}
	if err := yaml.Unmarshal(data, &prefs); err != nil {
		return nil
	}
	return prefs.Preferences
}

// extractSourceFromFrontmatter reads the "source" field from a SKILL.md in a directory.
func (l *Loader) extractSourceFromFrontmatter(dirPath string) string {
	data, err := os.ReadFile(filepath.Clean(filepath.Join(dirPath, "SKILL.md"))) // #nosec G304 -- dirPath from skillsDir scan
	if err != nil {
		return ""
	}

	lines := strings.Split(string(data), "\n")
	inFrontmatter := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			}
			break
		}
		if inFrontmatter && strings.HasPrefix(trimmed, "source:") {
			val := strings.TrimPrefix(trimmed, "source:")
			val = strings.TrimSpace(val)
			val = strings.Trim(val, "\"'")
			return val
		}
	}
	return ""
}

func (l *Loader) loadSkillFromPackage(dirPath string) (*Skill, error) {
	mainSkillFile := filepath.Join(dirPath, "SKILL.md")

	// This ensures we are dealing with a valid skill package
	if _, err := os.Stat(mainSkillFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("directory '%s' found but missing 'SKILL.md'", filepath.Base(dirPath))
	}

	// Load Metadata and Content from the main SKILL.md
	frontmatter, content, err := l.parseMarkdownFile(mainSkillFile)
	if err != nil {
		return nil, err
	}

	absDir, _ := filepath.Abs(dirPath)

	skill := &Skill{
		Dir:       absDir,
		Content:   content,
		Subskills: make(map[string]string),
		Scripts:   make(map[string]string),
	}

	// Parse YAML frontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), skill); err != nil {
		return nil, fmt.Errorf("failed to parse frontmatter in '%s': %w", mainSkillFile, err)
	}

	// Fallback name if missing in frontmatter
	if skill.Name == "" {
		skill.Name = filepath.Base(dirPath)
	}

	// --- 1. Map Sub-skills (.md files) ---
	entries, err := os.ReadDir(dirPath)
	if err == nil {
		for _, entry := range entries {
			// Skip directories, non-md files, and the main SKILL.md itself
			if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".md") && !strings.EqualFold(entry.Name(), "SKILL.md") {
				fullPath := filepath.Join(absDir, entry.Name())
				skill.Subskills[entry.Name()] = fullPath
			}
		}
	}

	// --- 2. Map Scripts (scripts/ folder) ---
	scriptsDir := filepath.Join(dirPath, "scripts")
	if info, err := os.Stat(scriptsDir); err == nil && info.IsDir() {
		scriptEntries, err := os.ReadDir(scriptsDir)
		if err == nil {
			for _, sc := range scriptEntries {
				if !sc.IsDir() {
					// We only care about executable-like scripts, but generally mapping all files in 'scripts' is safer for flexibility
					fullPath := filepath.Join(absDir, "scripts", sc.Name())
					// Key is relative path for display, Value is absolute path for execution
					displayKey := filepath.Join("scripts", sc.Name())
					skill.Scripts[displayKey] = fullPath
				}
			}
		}
	}

	return skill, nil
}

// loadSkillFile loads a V1 skill (Single file)
func (l *Loader) loadSkillFile(path string) (*Skill, error) {
	frontmatter, content, err := l.parseMarkdownFile(path)
	if err != nil {
		return nil, err
	}

	absPath, _ := filepath.Abs(path)
	parentDir := filepath.Dir(absPath)

	skill := &Skill{
		Dir:       parentDir, // Even single files reside in a dir
		Path:      absPath,
		Content:   content,
		Subskills: make(map[string]string), // Empty for single file
		Scripts:   make(map[string]string), // Empty for single file
	}

	if err := yaml.Unmarshal([]byte(frontmatter), skill); err != nil {
		return nil, fmt.Errorf("failed to parse skill frontmatter: %w", err)
	}

	if skill.Name == "" {
		skill.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}

	return skill, nil
}

// loadAgentFile loads an agent from a markdown file
func (l *Loader) loadAgentFile(path string) (*Agent, error) {
	frontmatter, content, err := l.parseMarkdownFile(path)
	if err != nil {
		return nil, err
	}

	absPath, _ := filepath.Abs(path)

	agent := &Agent{
		Path:    absPath,
		Content: content,
	}

	if err := yaml.Unmarshal([]byte(frontmatter), agent); err != nil {
		return nil, fmt.Errorf("failed to parse agent frontmatter: %w", err)
	}

	if agent.Name == "" {
		agent.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}

	return agent, nil
}

// parseMarkdownFile extracts YAML frontmatter and content from a markdown file.
// Robust to different line endings and whitespace.
func (l *Loader) parseMarkdownFile(path string) (string, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var frontmatter strings.Builder
	var content strings.Builder

	inFrontmatter := false
	frontmatterDone := false
	lineNum := 0

	for scanner.Scan() {
		line := scanner.Text()
		lineNum++
		trimmedLine := strings.TrimSpace(line)

		// Check for Frontmatter delimiters "---"
		if trimmedLine == "---" {
			if !inFrontmatter && !frontmatterDone && lineNum == 1 {
				// Start of file
				inFrontmatter = true
				continue
			} else if inFrontmatter {
				// End of frontmatter block
				inFrontmatter = false
				frontmatterDone = true
				continue
			}
		}

		if inFrontmatter {
			frontmatter.WriteString(line)
			frontmatter.WriteString("\n")
		} else {
			// If we never found frontmatter but we are reading content, just append
			// Or if we finished frontmatter
			content.WriteString(line)
			content.WriteString("\n")
		}
	}

	if err := scanner.Err(); err != nil {
		return "", "", fmt.Errorf("error reading file: %w", err)
	}

	return frontmatter.String(), strings.TrimSpace(content.String()), nil
}

// EnsureDirectories creates the agents and skills directories if they don't exist
func (l *Loader) EnsureDirectories() error {
	if err := os.MkdirAll(l.agentsDir, 0o700); err != nil {
		return fmt.Errorf("failed to create agents directory: %w", err)
	}
	if err := os.MkdirAll(l.skillsDir, 0o700); err != nil {
		return fmt.Errorf("failed to create skills directory: %w", err)
	}
	return nil
}

// GetAgentsDir returns the path to the agents directory
func (l *Loader) GetAgentsDir() string {
	return l.agentsDir
}

// GetSkillsDir returns the path to the skills directory
func (l *Loader) GetSkillsDir() string {
	return l.skillsDir
}
