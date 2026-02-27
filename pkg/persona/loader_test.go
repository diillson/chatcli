/*
 * ChatCLI - Persona System Tests
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package persona

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestParseMarkdownFile(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	loader := NewLoader(logger)

	// Create a temporary test file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test-agent.md")

	content := `---
name: "test-agent"
description: "A test agent"
skills:
  - skill-1
  - skill-2
plugins:
  - "@coder"
---
# Test Agent

This is the base content for the test agent.
`
	err := os.WriteFile(testFile, []byte(content), 0644)
	assert.NoError(t, err)

	// Test parsing
	frontmatter, body, err := loader.parseMarkdownFile(testFile)
	assert.NoError(t, err)
	assert.Contains(t, frontmatter, "name:")
	assert.Contains(t, body, "Test Agent")
	assert.Contains(t, body, "base content")
}

func TestLoadAgentFile(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	loader := NewLoader(logger)

	// Create a temporary test file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test-agent.md")

	content := `---
name: "my-agent"
description: "A test agent description"
skills:
  - clean-code
  - error-handling
plugins:
  - "@coder"
---
# My Agent Personality

You are a helpful assistant.
`
	err := os.WriteFile(testFile, []byte(content), 0644)
	assert.NoError(t, err)

	// Test loading
	agent, err := loader.loadAgentFile(testFile)
	assert.NoError(t, err)
	assert.NotNil(t, agent)

	assert.Equal(t, "my-agent", agent.Name)
	assert.Equal(t, "A test agent description", agent.Description)
	assert.Len(t, agent.Skills, 2)
	assert.Contains(t, agent.Skills, "clean-code")
	assert.Contains(t, agent.Skills, "error-handling")
	assert.Len(t, agent.Plugins, 1)
	assert.Contains(t, agent.Content, "helpful assistant")
}

func TestLoadSkillFile(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	loader := NewLoader(logger)

	// Create a temporary test file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test-skill.md")

	content := `---
name: "clean-code"
description: "Clean code principles"
---
# Clean Code Rules

1. Use meaningful names
2. Keep functions small
3. Don't repeat yourself
`
	err := os.WriteFile(testFile, []byte(content), 0644)
	assert.NoError(t, err)

	// Test loading
	skill, err := loader.loadSkillFile(testFile)
	assert.NoError(t, err)
	assert.NotNil(t, skill)

	assert.Equal(t, "clean-code", skill.Name)
	assert.Equal(t, "Clean code principles", skill.Description)
	assert.Contains(t, skill.Content, "meaningful names")
}

func TestFileWithoutFrontmatter(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	loader := NewLoader(logger)

	// Create a temporary test file without frontmatter
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "no-frontmatter.md")

	content := `# Simple Agent

This file has no frontmatter.
`
	err := os.WriteFile(testFile, []byte(content), 0644)
	assert.NoError(t, err)

	// Test loading - should use filename as name
	agent, err := loader.loadAgentFile(testFile)
	assert.NoError(t, err)
	assert.NotNil(t, agent)
	assert.Equal(t, "no-frontmatter", agent.Name) // Derived from filename
}

// --- Project-level agent tests ---

// setupProjectAndGlobal creates a temp structure with project-local and global agent dirs.
// Returns the loader with projectDir set, and cleanup is handled by t.TempDir().
func setupProjectAndGlobal(t *testing.T) (*Loader, string, string) {
	t.Helper()
	logger := zap.NewNop()

	root := t.TempDir()
	globalAgents := filepath.Join(root, "global", "agents")
	globalSkills := filepath.Join(root, "global", "skills")
	projectDir := filepath.Join(root, "project")
	projectAgents := filepath.Join(projectDir, ".agent", "agents")

	for _, d := range []string{globalAgents, globalSkills, projectAgents} {
		assert.NoError(t, os.MkdirAll(d, 0o755))
	}

	loader := &Loader{
		logger:     logger,
		agentsDir:  globalAgents,
		skillsDir:  globalSkills,
		projectDir: projectDir,
	}

	return loader, globalAgents, projectAgents
}

func writeAgent(t *testing.T, dir, filename, name, desc string) {
	t.Helper()
	content := fmt.Sprintf("---\nname: %q\ndescription: %q\n---\n# %s\nContent for %s.\n", name, desc, name, name)
	assert.NoError(t, os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644))
}

func TestListAgents_ProjectPrecedence(t *testing.T) {
	loader, globalDir, projectDir := setupProjectAndGlobal(t)

	// Same agent name in both — project should win
	writeAgent(t, globalDir, "devops.md", "devops", "Global devops")
	writeAgent(t, projectDir, "devops.md", "devops", "Project devops")

	agents, err := loader.ListAgents()
	assert.NoError(t, err)
	assert.Len(t, agents, 1)
	assert.Equal(t, "devops", agents[0].Name)
	assert.Equal(t, "Project devops", agents[0].Description)
}

func TestListAgents_MergesProjectAndGlobal(t *testing.T) {
	loader, globalDir, projectDir := setupProjectAndGlobal(t)

	writeAgent(t, globalDir, "go-expert.md", "go-expert", "Global Go expert")
	writeAgent(t, projectDir, "devops.md", "devops", "Project devops")

	agents, err := loader.ListAgents()
	assert.NoError(t, err)
	assert.Len(t, agents, 2)

	names := map[string]bool{}
	for _, a := range agents {
		names[a.Name] = true
	}
	assert.True(t, names["devops"])
	assert.True(t, names["go-expert"])
}

func TestGetAgent_ProjectFirst(t *testing.T) {
	loader, globalDir, projectDir := setupProjectAndGlobal(t)

	writeAgent(t, globalDir, "devops.md", "devops", "Global devops")
	writeAgent(t, projectDir, "devops.md", "devops", "Project devops")

	agent, err := loader.GetAgent("devops")
	assert.NoError(t, err)
	assert.Equal(t, "Project devops", agent.Description)
}

func TestGetAgent_FallsBackToGlobal(t *testing.T) {
	loader, globalDir, _ := setupProjectAndGlobal(t)

	writeAgent(t, globalDir, "go-expert.md", "go-expert", "Global Go expert")

	agent, err := loader.GetAgent("go-expert")
	assert.NoError(t, err)
	assert.Equal(t, "Global Go expert", agent.Description)
}

func TestListAgents_NoProjectDir(t *testing.T) {
	logger := zap.NewNop()
	root := t.TempDir()
	globalAgents := filepath.Join(root, "agents")
	assert.NoError(t, os.MkdirAll(globalAgents, 0o755))

	loader := &Loader{
		logger:    logger,
		agentsDir: globalAgents,
		skillsDir: filepath.Join(root, "skills"),
		// projectDir intentionally empty
	}

	writeAgent(t, globalAgents, "go-expert.md", "go-expert", "Go expert")

	agents, err := loader.ListAgents()
	assert.NoError(t, err)
	assert.Len(t, agents, 1)
	assert.Equal(t, "go-expert", agents[0].Name)
}

func TestGetAgent_MetadataNameMatch(t *testing.T) {
	loader, globalDir, _ := setupProjectAndGlobal(t)

	// Filename doesn't match agent name in metadata
	writeAgent(t, globalDir, "my-devops-agent.md", "devops", "DevOps via metadata")

	agent, err := loader.GetAgent("devops")
	assert.NoError(t, err)
	assert.Equal(t, "DevOps via metadata", agent.Description)
}

func TestGetAgent_NotFound(t *testing.T) {
	loader, _, _ := setupProjectAndGlobal(t)

	_, err := loader.GetAgent("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "agent not found")
}
