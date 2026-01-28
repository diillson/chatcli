/*
 * ChatCLI - Persona System Tests
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package persona

import (
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
