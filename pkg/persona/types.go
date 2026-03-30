/*
 * ChatCLI - Persona System
 * pkg/persona/types.go
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package persona

import (
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// StringList é um tipo auxiliar que permite que o usuário escreva no YAML
// tanto uma lista de strings quanto uma string única separada por vírgulas.
// Ex: skills: [a, b]  OU  skills: a, b, c
type StringList []string

// UnmarshalYAML implementa a interface yaml.Unmarshaler para flexibilidade
func (sl *StringList) UnmarshalYAML(value *yaml.Node) error {
	// Caso 1: É uma sequência YAML (lista com - ou [])
	if value.Kind == yaml.SequenceNode {
		var temp []string
		if err := value.Decode(&temp); err != nil {
			return err
		}
		*sl = temp
		return nil
	}

	// Caso 2: É um escalar (string separada por vírgulas)
	if value.Kind == yaml.ScalarNode {
		parts := strings.Split(value.Value, ",")
		var result []string
		for _, p := range parts {
			clean := strings.TrimSpace(p)
			if clean != "" {
				result = append(result, clean)
			}
		}
		*sl = result
		return nil
	}

	// Caso 3: Nulo ou vazio
	if value.Kind == yaml.AliasNode || value.Kind == 0 {
		*sl = []string{}
		return nil
	}

	return nil // Ignora outros tipos ou retorna erro se preferir ser estrito
}

// Agent represents a loadable persona with skills
type Agent struct {
	// Metadata from frontmatter
	Name        string     `json:"name" yaml:"name"`
	Description string     `json:"description" yaml:"description"`
	Skills      StringList `json:"skills" yaml:"skills"`   // Usando StringList para robustez
	Plugins     StringList `json:"plugins" yaml:"plugins"` // Usando StringList para robustez
	Tools       StringList `json:"tools" yaml:"tools"`     // Allowed tools for worker mode (Read, Grep, Glob, Bash, Write, Edit)
	Model       string     `json:"model" yaml:"model"`

	// Content is the markdown body (without frontmatter)
	Content string `json:"-" yaml:"-"`

	// Path to the source file
	Path string `json:"-" yaml:"-"`
}

// Skill represents a reusable knowledge/compliance module
type Skill struct {
	// Metadata from frontmatter
	Name        string     `json:"name" yaml:"name"`
	Description string     `json:"description" yaml:"description"`
	Tools       StringList `json:"allowed-tools" yaml:"allowed-tools"` // Usando StringList

	// Advanced frontmatter fields
	Model    string     `json:"model,omitempty" yaml:"model"`       // Preferred model for this skill (e.g., "sonnet", "opus")
	Effort   string     `json:"effort,omitempty" yaml:"effort"`     // Effort level: "low", "medium", "high", "max"
	Paths    StringList `json:"paths,omitempty" yaml:"paths"`       // Glob patterns for file matching (lazy load)
	Triggers StringList `json:"triggers,omitempty" yaml:"triggers"` // Keywords that auto-activate this skill
	Tags     StringList `json:"tags,omitempty" yaml:"tags"`         // Categorization tags
	Category string     `json:"category,omitempty" yaml:"category"` // Skill category (e.g., "code-quality", "testing")
	Version  string     `json:"version,omitempty" yaml:"version"`   // Skill version
	Author   string     `json:"author,omitempty" yaml:"author"`     // Skill author

	// Behavior controls
	UserInvocable          bool   `json:"user-invocable,omitempty" yaml:"user-invocable"`                     // Can be invoked with /skill-name
	DisableModelInvocation bool   `json:"disable-model-invocation,omitempty" yaml:"disable-model-invocation"` // Only manual invocation
	ArgumentHint           string `json:"argument-hint,omitempty" yaml:"argument-hint"`                       // Shows expected args in autocomplete

	// Content is the markdown body (without frontmatter)
	Content string `json:"-" yaml:"-"`

	// Path to the root directory of this skill
	Dir string `json:"-" yaml:"-"`

	// Path to the source file (legacy or main file)
	Path string `json:"-" yaml:"-"`

	// Subskills mapeia "nomearquivo.md" -> caminho absoluto
	Subskills map[string]string `json:"subskills" yaml:"-"`

	// Scripts mapeia "scripts/nomescript.py" -> caminho absoluto
	Scripts map[string]string `json:"scripts" yaml:"-"`
}

// MatchesTrigger checks if the given text contains any of the skill's trigger keywords.
func (s *Skill) MatchesTrigger(text string) bool {
	if len(s.Triggers) == 0 {
		return false
	}
	textLower := strings.ToLower(text)
	for _, trigger := range s.Triggers {
		if strings.Contains(textLower, strings.ToLower(trigger)) {
			return true
		}
	}
	return false
}

// MatchesPath checks if any of the given file paths match the skill's path patterns.
func (s *Skill) MatchesPath(filePaths []string) bool {
	if len(s.Paths) == 0 {
		return false
	}
	for _, pattern := range s.Paths {
		for _, fp := range filePaths {
			if matched, _ := filepath.Match(pattern, fp); matched {
				return true
			}
			if matched, _ := filepath.Match(pattern, filepath.Base(fp)); matched {
				return true
			}
			// Support ** prefix matching
			if strings.Contains(pattern, "**") {
				prefix := strings.Split(pattern, "**")[0]
				if strings.HasPrefix(fp, prefix) {
					return true
				}
			}
		}
	}
	return false
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
	ActiveAgents  []string // List of active agent names
	SkillsLoaded  []string
	SkillsMissing []string
	FullPrompt    string
}
