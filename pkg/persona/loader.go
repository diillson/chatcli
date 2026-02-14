/*
 * ChatCLI - Persona System
 * pkg/persona/loader.go
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
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

// ListAgents returns all available agents scanning global directory
func (l *Loader) ListAgents() ([]*Agent, error) {
	var agents []*Agent

	// Ensure directory exists
	if _, err := os.Stat(l.agentsDir); os.IsNotExist(err) {
		return agents, nil // No agents yet
	}

	entries, err := os.ReadDir(l.agentsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read agents directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		path := filepath.Join(l.agentsDir, entry.Name())
		agent, err := l.loadAgentFile(path)
		if err != nil {
			l.logger.Warn("Failed to load agent file, skipping",
				zap.String("path", path),
				zap.Error(err))
			continue
		}
		agents = append(agents, agent)
	}

	return agents, nil
}

// ListSkills returns all available skills (names only for listing purposes)
// It performs a shallow scan of both project-local and global directories.
func (l *Loader) ListSkills() ([]*Skill, error) {
	var skills []*Skill
	seen := make(map[string]bool)

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
			name := strings.TrimSuffix(entry.Name(), ".md")
			fullPath := filepath.Join(basePath, entry.Name())

			// Skip if already loaded (Project overrides Global)
			if seen[name] {
				continue
			}

			var skill *Skill
			var loadErr error

			if entry.IsDir() {
				// Try to load as a Package (V2)
				skill, loadErr = l.loadSkillFromPackage(fullPath)
			} else if strings.HasSuffix(entry.Name(), ".md") {
				// Try to load as a File (V1)
				skill, loadErr = l.loadSkillFile(fullPath)
			}

			if loadErr == nil && skill != nil {
				skills = append(skills, skill)
				seen[skill.Name] = true
			} else if loadErr != nil {
				// Only log warning if it looked like a skill (has SKILL.md or is .md)
				l.logger.Debug("Skipping entry in skills dir", zap.String("entry", entry.Name()), zap.Error(loadErr))
			}
		}
	}

	// 1. Load from project directory first (higher priority)
	if l.projectDir != "" {
		projectSkillsDir := filepath.Join(l.projectDir, ".agent", "skills")
		scanDir(projectSkillsDir)
	}

	// 2. Load from global directory
	scanDir(l.skillsDir)

	return skills, nil
}

// GetAgent returns an agent by name
func (l *Loader) GetAgent(name string) (*Agent, error) {
	// Try exact filename first at global path
	path := filepath.Join(l.agentsDir, name+".md")
	if _, err := os.Stat(path); err == nil {
		return l.loadAgentFile(path)
	}

	// Fallback: Search by "name" metadata inside files
	// This is slower but useful if filename != agent name
	agents, err := l.ListAgents()
	if err != nil {
		return nil, err
	}

	for _, a := range agents {
		if strings.EqualFold(a.Name, name) {
			return a, nil
		}
	}

	return nil, fmt.Errorf("agent not found: %s", name)
}

// GetSkill locates and loads a skill by name.
// It prioritizes:
// 1. Project-local Package (Folder with SKILL.md)
// 2. Project-local File (.md)
// 3. Global Package
// 4. Global File
func (l *Loader) GetSkill(name string) (*Skill, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("skill name cannot be empty")
	}

	// Helper to check package vs file
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

	// 1. Check Project Directory
	if l.projectDir != "" {
		projectSkillsDir := filepath.Join(l.projectDir, ".agent", "skills")
		if skill, err := checkLocation(projectSkillsDir); err == nil {
			return skill, nil
		}
	}

	// 2. Check Global Directory
	if skill, err := checkLocation(l.skillsDir); err == nil {
		return skill, nil
	}

	return nil, fmt.Errorf("skill not found: '%s' (checked local and global)", name)
}

// loadSkillFromPackage loads a V2 skill structure (Directory based)
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
