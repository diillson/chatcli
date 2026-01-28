/*
 * ChatCLI - Persona System
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package persona

// Agent represents a loadable persona with skills
type Agent struct {
	// Metadata from frontmatter
	Name        string   `json:"name" yaml:"name"`
	Description string   `json:"description" yaml:"description"`
	Skills      []string `json:"skills" yaml:"skills"`
	Plugins     []string `json:"plugins" yaml:"plugins"`

	// Content is the markdown body (without frontmatter)
	Content string `json:"-" yaml:"-"`

	// Path to the source file
	Path string `json:"-" yaml:"-"`
}

// Skill represents a reusable knowledge/compliance module
type Skill struct {
	// Metadata from frontmatter
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`

	// Content is the markdown body (without frontmatter)
	Content string `json:"-" yaml:"-"`

	// Path to the source file
	Path string `json:"-" yaml:"-"`
}

// LoadResult represents the result of loading an agent with its skills
type LoadResult struct {
	Agent         *Agent
	LoadedSkills  []string // Skills that were successfully loaded
	MissingSkills []string // Skills that were not found
	Warnings      []string // Non-fatal issues
}

// ComposedPrompt represents the final assembled system prompt
type ComposedPrompt struct {
	AgentName     string
	SkillsLoaded  []string
	SkillsMissing []string
	FullPrompt    string
}
