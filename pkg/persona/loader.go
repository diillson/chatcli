/*
 * ChatCLI - Persona System
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
	projectDir string // Optional project-local directory
}

// NewLoader creates a new persona loader
func NewLoader(logger *zap.Logger) *Loader {
	home, _ := os.UserHomeDir()
	baseDir := filepath.Join(home, ".chatcli")

	return &Loader{
		logger:    logger,
		agentsDir: filepath.Join(baseDir, "agents"),
		skillsDir: filepath.Join(baseDir, "skills"),
	}
}

// SetProjectDir sets an optional project-local directory for skills
func (l *Loader) SetProjectDir(dir string) {
	l.projectDir = dir
}

// ListAgents returns all available agents
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
			l.logger.Warn("Failed to load agent",
				zap.String("path", path),
				zap.Error(err))
			continue
		}
		agents = append(agents, agent)
	}

	return agents, nil
}

// ListSkills returns all available skills
func (l *Loader) ListSkills() ([]*Skill, error) {
	var skills []*Skill
	seen := make(map[string]bool)

	// Load from project directory first (higher priority)
	if l.projectDir != "" {
		projectSkillsDir := filepath.Join(l.projectDir, ".chatcli", "skills")
		projectSkills, _ := l.loadSkillsFromDir(projectSkillsDir)
		for _, s := range projectSkills {
			skills = append(skills, s)
			seen[s.Name] = true
		}
	}

	// Load from global directory
	globalSkills, _ := l.loadSkillsFromDir(l.skillsDir)
	for _, s := range globalSkills {
		if !seen[s.Name] {
			skills = append(skills, s)
		}
	}

	return skills, nil
}

func (l *Loader) loadSkillsFromDir(dir string) ([]*Skill, error) {
	var skills []*Skill

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return skills, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		skill, err := l.loadSkillFile(path)
		if err != nil {
			l.logger.Warn("Failed to load skill",
				zap.String("path", path),
				zap.Error(err))
			continue
		}
		skills = append(skills, skill)
	}

	return skills, nil
}

// GetAgent returns an agent by name
func (l *Loader) GetAgent(name string) (*Agent, error) {
	// Try exact filename first
	path := filepath.Join(l.agentsDir, name+".md")
	if _, err := os.Stat(path); err == nil {
		return l.loadAgentFile(path)
	}

	// Search by name field in frontmatter
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

// GetSkill returns a skill by name (project dir has priority)
func (l *Loader) GetSkill(name string) (*Skill, error) {
	// Try project directory first
	if l.projectDir != "" {
		projectPath := filepath.Join(l.projectDir, ".chatcli", "skills", name+".md")
		if _, err := os.Stat(projectPath); err == nil {
			return l.loadSkillFile(projectPath)
		}
	}

	// Try global directory
	path := filepath.Join(l.skillsDir, name+".md")
	if _, err := os.Stat(path); err == nil {
		return l.loadSkillFile(path)
	}

	// Search by name field in frontmatter
	skills, err := l.ListSkills()
	if err != nil {
		return nil, err
	}

	for _, s := range skills {
		if strings.EqualFold(s.Name, name) {
			return s, nil
		}
	}

	return nil, fmt.Errorf("skill not found: %s", name)
}

// loadAgentFile loads an agent from a markdown file
func (l *Loader) loadAgentFile(path string) (*Agent, error) {
	frontmatter, content, err := l.parseMarkdownFile(path)
	if err != nil {
		return nil, err
	}

	agent := &Agent{
		Path:    path,
		Content: content,
	}

	// Parse YAML frontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), agent); err != nil {
		return nil, fmt.Errorf("failed to parse agent frontmatter: %w", err)
	}

	// If name not set in frontmatter, use filename
	if agent.Name == "" {
		agent.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}

	return agent, nil
}

// loadSkillFile loads a skill from a markdown file
func (l *Loader) loadSkillFile(path string) (*Skill, error) {
	frontmatter, content, err := l.parseMarkdownFile(path)
	if err != nil {
		return nil, err
	}

	skill := &Skill{
		Path:    path,
		Content: content,
	}

	// Parse YAML frontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), skill); err != nil {
		return nil, fmt.Errorf("failed to parse skill frontmatter: %w", err)
	}

	// If name not set in frontmatter, use filename
	if skill.Name == "" {
		skill.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}

	return skill, nil
}

// parseMarkdownFile extracts YAML frontmatter and content from a markdown file
func (l *Loader) parseMarkdownFile(path string) (string, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var frontmatter, content strings.Builder
	inFrontmatter := false
	frontmatterDone := false
	lineNum := 0

	for scanner.Scan() {
		line := scanner.Text()
		lineNum++

		// Detect frontmatter boundaries
		if strings.TrimSpace(line) == "---" {
			if !inFrontmatter && lineNum == 1 {
				inFrontmatter = true
				continue
			} else if inFrontmatter {
				inFrontmatter = false
				frontmatterDone = true
				continue
			}
		}

		if inFrontmatter {
			frontmatter.WriteString(line)
			frontmatter.WriteString("\n")
		} else if frontmatterDone || lineNum > 1 {
			content.WriteString(line)
			content.WriteString("\n")
		}
	}

	if err := scanner.Err(); err != nil {
		return "", "", err
	}

	return frontmatter.String(), strings.TrimSpace(content.String()), nil
}

// EnsureDirectories creates the agents and skills directories if they don't exist
func (l *Loader) EnsureDirectories() error {
	if err := os.MkdirAll(l.agentsDir, 0755); err != nil {
		return fmt.Errorf("failed to create agents directory: %w", err)
	}
	if err := os.MkdirAll(l.skillsDir, 0755); err != nil {
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
