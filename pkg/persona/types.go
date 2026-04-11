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

	// Per-agent LLM preferences (Fase: multi-agent parity with skills).
	// Model declares the ideal model id for when this agent runs as a
	// worker; the dispatcher resolves it through the same cross-provider
	// router used by skill hints (catalog → family heuristic → optimistic
	// fallback). Effort maps to extended thinking / reasoning_effort for
	// providers that support it.
	//
	// Both fields are optional: empty = inherit the user's active model
	// and no effort hint. When either is set, cli.Client is never
	// mutated — the swap applies only to this worker's turns.
	Model  string `json:"model,omitempty" yaml:"model"`   // Preferred model id (e.g. "opus-4-6")
	Effort string `json:"effort,omitempty" yaml:"effort"` // "low", "medium", "high", "max"

	// Optional metadata mirroring the skill frontmatter — useful for
	// `/agent list`, catalog display, and future registry search.
	Category string     `json:"category,omitempty" yaml:"category"`
	Version  string     `json:"version,omitempty" yaml:"version"`
	Author   string     `json:"author,omitempty" yaml:"author"`
	Tags     StringList `json:"tags,omitempty" yaml:"tags"`

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

// MatchesPath checks if any of the given file paths match any of the skill's
// path glob patterns. Supports:
//
//   - "*"  — matches within a single path segment (like filepath.Match)
//   - "?"  — matches a single character within a segment
//   - "**" — matches zero or more path segments (recursive)
//
// Matching is tried against both the full forward-slash path and the basename,
// so a pattern like "*_test.go" still matches "pkg/foo/bar_test.go".
func (s *Skill) MatchesPath(filePaths []string) bool {
	if len(s.Paths) == 0 || len(filePaths) == 0 {
		return false
	}
	for _, pattern := range s.Paths {
		pattern = normalizeSlashes(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		for _, fp := range filePaths {
			fp = normalizeSlashes(fp)
			if globMatch(pattern, fp) {
				return true
			}
			if globMatch(pattern, filepath.Base(fp)) {
				return true
			}
		}
	}
	return false
}

// normalizeSlashes converts OS-specific path separators to forward slashes
// so glob patterns are portable between Linux/macOS/Windows.
func normalizeSlashes(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

// globMatch reports whether name matches the glob pattern with support for
// the "**" doublestar (matches zero or more path segments). The implementation
// is segment-based to handle "src/**/*.ts" and "**/foo.go" correctly.
func globMatch(pattern, name string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

// matchSegments is a recursive backtracking matcher over path segments.
func matchSegments(pParts, nParts []string) bool {
	for len(pParts) > 0 {
		seg := pParts[0]
		if seg == "**" {
			// Collapse consecutive "**" tokens.
			for len(pParts) > 1 && pParts[1] == "**" {
				pParts = pParts[1:]
			}
			// "**" at the end matches everything remaining (including empty).
			if len(pParts) == 1 {
				return true
			}
			rest := pParts[1:]
			// Try consuming 0..len(nParts) leading segments.
			for i := 0; i <= len(nParts); i++ {
				if matchSegments(rest, nParts[i:]) {
					return true
				}
			}
			return false
		}
		if len(nParts) == 0 {
			return false
		}
		matched, err := filepath.Match(seg, nParts[0])
		if err != nil || !matched {
			return false
		}
		pParts = pParts[1:]
		nParts = nParts[1:]
	}
	return len(nParts) == 0
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
