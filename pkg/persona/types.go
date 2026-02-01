/*
 * ChatCLI - Persona System
 * pkg/persona/types.go
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package persona

import (
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
