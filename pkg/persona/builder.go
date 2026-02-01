/*
 * ChatCLI - Persona System
 * pkg/persona/builder.go
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package persona

import (
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"
)

// Builder assembles system prompts from agents and skills
type Builder struct {
	logger *zap.Logger
	loader *Loader
}

// NewBuilder creates a new prompt builder
func NewBuilder(logger *zap.Logger, loader *Loader) *Builder {
	return &Builder{
		logger: logger,
		loader: loader,
	}
}

// BuildMultiAgentPrompt assembles a prompt for multiple agents simultaneously
func (b *Builder) BuildMultiAgentPrompt(agents []*Agent) (*ComposedPrompt, error) {
	result := &ComposedPrompt{
		ActiveAgents:  []string{},
		SkillsLoaded:  []string{},
		SkillsMissing: []string{},
	}

	var promptParts []string

	// 1. Collective Role Definition
	agentNames := []string{}
	for _, a := range agents {
		agentNames = append(agentNames, strings.ToUpper(a.Name))
		result.ActiveAgents = append(result.ActiveAgents, a.Name)
	}

	promptParts = append(promptParts, "============================================================")
	promptParts = append(promptParts, ">>> [MULTI-AGENT SYSTEM ACTIVATED]")
	promptParts = append(promptParts, fmt.Sprintf("ACTIVE EXPERTS: %s", strings.Join(agentNames, ", ")))
	promptParts = append(promptParts, "You must act as a unified intelligence incorporating the expertise of all active agents.")
	promptParts = append(promptParts, "============================================================")
	promptParts = append(promptParts, "")

	// 2. Individual Directives (Merge content/descriptions)
	promptParts = append(promptParts, "### AGENT DIRECTIVES ###")
	for _, agent := range agents {
		promptParts = append(promptParts, fmt.Sprintf("--- DIRECTIVES FOR: %s ---", strings.ToUpper(agent.Name)))
		if agent.Description != "" {
			promptParts = append(promptParts, fmt.Sprintf("ROLE: %s", agent.Description))
		}
		if agent.Content != "" {
			promptParts = append(promptParts, agent.Content)
		}
		promptParts = append(promptParts, "")
	}

	// 3. Consolidated Skills (Deduplication Logic)
	uniqueSkills := make(map[string]bool)
	uniquePlugins := make(map[string]bool)

	// Collect distinct skill names
	for _, agent := range agents {
		for _, s := range agent.Skills {
			uniqueSkills[s] = true
		}
		for _, p := range agent.Plugins {
			uniquePlugins[p] = true
		}
	}

	if len(uniqueSkills) > 0 {
		promptParts = append(promptParts, "########################################################")
		promptParts = append(promptParts, "### CONSOLIDATED KNOWLEDGE SKILLS ###")
		promptParts = append(promptParts, "#########################################################")

		// Sort for deterministic order
		var sortedSkills []string
		for k := range uniqueSkills {
			sortedSkills = append(sortedSkills, k)
		}
		sort.Strings(sortedSkills)

		for idx, skillName := range sortedSkills {
			skillName = strings.TrimSpace(skillName)
			if skillName == "" {
				continue
			}

			skill, err := b.loader.GetSkill(skillName)
			if err != nil {
				b.logger.Warn("Skill not found", zap.String("skill", skillName))
				result.SkillsMissing = append(result.SkillsMissing, skillName)
				continue
			}

			// Add Skill Content
			promptParts = append(promptParts, fmt.Sprintf("\n>>> SKILL Module #%d: %s", idx+1, strings.ToUpper(skill.Name)))
			promptParts = append(promptParts, skill.Content)

			// Handle Subskills/Scripts paths
			b.appendSkillResources(&promptParts, skill)

			result.SkillsLoaded = append(result.SkillsLoaded, skill.Name)
		}
	}

	// 4. Consolidated Plugins
	if len(uniquePlugins) > 0 {
		promptParts = append(promptParts, "\n### AVAILABLE TOOLS (PLUGINS) ###")
		for p := range uniquePlugins {
			promptParts = append(promptParts, fmt.Sprintf("- %s", p))
		}
	}

	promptParts = append(promptParts, "\n### SYSTEM INSTRUCTION ###")
	promptParts = append(promptParts, "Synthesize the perspectives of all active agents to answer the user.")

	result.FullPrompt = strings.Join(promptParts, "\n")
	return result, nil
}

// BuildSystemPrompt legacy wrapper for single agent
func (b *Builder) BuildSystemPrompt(agentName string) (*ComposedPrompt, error) {
	agent, err := b.loader.GetAgent(agentName)
	if err != nil {
		return nil, err
	}
	return b.BuildMultiAgentPrompt([]*Agent{agent})
}

// Helper to reuse resource appending logic
func (b *Builder) appendSkillResources(parts *[]string, skill *Skill) {
	hasSubskills := len(skill.Subskills) > 0
	hasScripts := len(skill.Scripts) > 0

	if hasSubskills || hasScripts {
		*parts = append(*parts, "")
		*parts = append(*parts, "--- 🛠️ SKILL RESOURCES (AVAILABLE ON DISK) ---")
		*parts = append(*parts, "You have access to the following resources within this skill package.")
		*parts = append(*parts, "DO NOT HALLUCINATE CONTENT. If you need details from these files, use your 'read' or 'exec' tools on the PATHS provided below.")

		if hasSubskills {
			*parts = append(*parts, "\n[KNOWLEDGE BASE (Markdown Documents)]:")

			// Sort for deterministic prompt
			var keys []string
			for k := range skill.Subskills {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			for _, name := range keys {
				absPath := skill.Subskills[name]
				*parts = append(*parts, fmt.Sprintf("- File: \"%s\"", name))
				*parts = append(*parts, fmt.Sprintf("  Path: \"%s\"", absPath))
			}
		}

		if hasScripts {
			*parts = append(*parts, "\n[EXECUTABLE SCRIPTS]:")
			*parts = append(*parts, "Use the command provided below to run these specialized scripts when needed.")

			// Sort keys
			var keys []string
			for k := range skill.Scripts {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			for _, name := range keys {
				absPath := skill.Scripts[name]
				cmd := inferExecutionCommand(name, absPath)
				*parts = append(*parts, fmt.Sprintf("- Script: \"%s\"", name))
				*parts = append(*parts, fmt.Sprintf("  Exec: `%s`", cmd))
			}
		}
		*parts = append(*parts, "--- END RESOURCES ---")
	}
}

// ValidateAgent checks if an agent and its linked skills are valid/exist
func (b *Builder) ValidateAgent(agentName string) ([]string, []string, error) {
	agent, err := b.loader.GetAgent(agentName)
	if err != nil {
		return nil, nil, err
	}

	var warnings, errorsList []string

	// Check if content is suspiciously empty
	if strings.TrimSpace(agent.Content) == "" {
		warnings = append(warnings, fmt.Sprintf("Agent '%s' has no base content (body is empty).", agentName))
	}

	// Check skills existence
	for _, skillName := range agent.Skills {
		skillName = strings.TrimSpace(skillName)
		if skillName == "" {
			continue
		}

		_, err := b.loader.GetSkill(skillName)
		if err != nil {
			errorsList = append(errorsList, fmt.Sprintf("Skill '%s' required by agent '%s' was not found.", skillName, agentName))
		}
	}

	return warnings, errorsList, nil
}

// inferExecutionCommand guesses the best command to run a script based on extension
func inferExecutionCommand(scriptName, absPath string) string {
	lower := strings.ToLower(scriptName)

	// Escape path just in case it has spaces (though simplified here)
	safePath := fmt.Sprintf("'%s'", absPath)

	switch {
	case strings.HasSuffix(lower, ".py"):
		return fmt.Sprintf("python %s", safePath)
	case strings.HasSuffix(lower, ".js"):
		return fmt.Sprintf("node %s", safePath)
	case strings.HasSuffix(lower, ".ts"):
		return fmt.Sprintf("npx ts-node %s", safePath)
	case strings.HasSuffix(lower, ".sh"):
		return fmt.Sprintf("bash %s", safePath)
	case strings.HasSuffix(lower, ".ps1"):
		return fmt.Sprintf("powershell -File %s", safePath)
	case strings.HasSuffix(lower, ".go"):
		return fmt.Sprintf("go run %s", safePath)
	case strings.HasSuffix(lower, ".rb"):
		return fmt.Sprintf("ruby %s", safePath)
	case strings.HasSuffix(lower, ".php"):
		return fmt.Sprintf("php %s", safePath)
	default:
		// Fallback: try to execute directly (shebang or binary)
		return safePath
	}
}
