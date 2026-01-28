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

// BuildSystemPrompt creates a composed system prompt from an agent and its skills.
// Uses a robust construction order: Role > Personality > Deep Skills Knowledge > Plugins.
func (b *Builder) BuildSystemPrompt(agentName string) (*ComposedPrompt, error) {
	// Load the agent
	agent, err := b.loader.GetAgent(agentName)
	if err != nil {
		return nil, fmt.Errorf("failed to load agent '%s': %w", agentName, err)
	}

	result := &ComposedPrompt{
		AgentName:     agent.Name,
		SkillsLoaded:  []string{},
		SkillsMissing: []string{},
	}

	var promptParts []string

	// =======================================================
	// PART 1: [ROLE] - Agent Identity & Context
	// =======================================================
	promptParts = append(promptParts, "====================================================")
	promptParts = append(promptParts, fmt.Sprintf(">>> [ROLE DEFINITION] AGENTE: %s", strings.ToUpper(agent.Name)))

	if agent.Description != "" {
		promptParts = append(promptParts, fmt.Sprintf("DESCRIPTION: %s", agent.Description))
	}

	promptParts = append(promptParts, "====================================================")
	promptParts = append(promptParts, "")

	// =======================================================
	// PART 2: [PERSONALITY] - Base Knowledge found in Agent File
	// =======================================================
	if agent.Content != "" {
		promptParts = append(promptParts, "### CORE PERSONALITY & DIRECTIVES ###")
		promptParts = append(promptParts, agent.Content)
		promptParts = append(promptParts, "")
	}

	// =======================================================
	// PART 3: [SKILLS] - Structured Specialized Knowledge
	// =======================================================
	if len(agent.Skills) > 0 {
		promptParts = append(promptParts, "#####################################################")
		promptParts = append(promptParts, "### INSTALLED SKILLS & CAPABILITIES ###")
		promptParts = append(promptParts, "#####################################################")
		promptParts = append(promptParts, "You have been equipped with the following specialized skills. Apply their rules rigorously.")
		promptParts = append(promptParts, "")

		for idx, skillName := range agent.Skills {
			skillName = strings.TrimSpace(skillName)
			if skillName == "" {
				continue
			}

			skill, err := b.loader.GetSkill(skillName)
			if err != nil {
				b.logger.Warn("Skill definition not found, skipping",
					zap.String("agent", agent.Name),
					zap.String("missing_skill", skillName),
					zap.Error(err))
				result.SkillsMissing = append(result.SkillsMissing, skillName)
				continue
			}

			// Add Skill Header
			promptParts = append(promptParts, strings.Repeat("=", 60))
			promptParts = append(promptParts, fmt.Sprintf("SKILL #%d: %s", idx+1, strings.ToUpper(skill.Name)))
			if skill.Description != "" {
				promptParts = append(promptParts, fmt.Sprintf("PURPOSE: %s", skill.Description))
			}
			promptParts = append(promptParts, strings.Repeat("=", 60))

			// Add Skill Main Content (Directives)
			promptParts = append(promptParts, skill.Content)

			// -----------------------------------------------------
			// POWER FEATURE: Injection of Subskills/Scripts Paths
			// -----------------------------------------------------
			hasSubskills := len(skill.Subskills) > 0
			hasScripts := len(skill.Scripts) > 0

			if hasSubskills || hasScripts {
				promptParts = append(promptParts, "")
				promptParts = append(promptParts, "--- 🛠️ SKILL RESOURCES (AVAILABLE ON DISK) ---")
				promptParts = append(promptParts, "You have access to the following resources within this skill package.")
				promptParts = append(promptParts, "DO NOT HALLUCINATE CONTENT. If you need details from these files, use your 'read' or 'exec' tools on the PATHS provided below.")

				if hasSubskills {
					promptParts = append(promptParts, "\n[KNOWLEDGE BASE (Markdown Documents)]:")

					// Sort for deterministic prompt
					var keys []string
					for k := range skill.Subskills {
						keys = append(keys, k)
					}
					sort.Strings(keys)

					for _, name := range keys {
						absPath := skill.Subskills[name]
						promptParts = append(promptParts, fmt.Sprintf("- File: \"%s\"", name))
						promptParts = append(promptParts, fmt.Sprintf("  Path: \"%s\"", absPath))
					}
				}

				if hasScripts {
					promptParts = append(promptParts, "\n[EXECUTABLE SCRIPTS]:")
					promptParts = append(promptParts, "Use the command provided below to run these specialized scripts when needed.")

					// Sort keys
					var keys []string
					for k := range skill.Scripts {
						keys = append(keys, k)
					}
					sort.Strings(keys)

					for _, name := range keys {
						absPath := skill.Scripts[name]
						cmd := inferExecutionCommand(name, absPath)
						promptParts = append(promptParts, fmt.Sprintf("- Script: \"%s\"", name))
						promptParts = append(promptParts, fmt.Sprintf("  Exec: `%s`", cmd))
					}
				}
				promptParts = append(promptParts, "--- END RESOURCES ---")
			}

			promptParts = append(promptParts, "") // Spacing between skills
			result.SkillsLoaded = append(result.SkillsLoaded, skill.Name)
		}
	}

	// =======================================================
	// PART 4: [PLUGINS] - Tool Hints
	// =======================================================
	if len(agent.Plugins) > 0 {
		promptParts = append(promptParts, "### ACTIVATED PLUGINS (CLI TOOLS) ###")
		promptParts = append(promptParts, "You have access to these external plugins. Invoke them using <tool_call> if in Coder mode, or execute blocks in Agent mode.")
		for _, plugin := range agent.Plugins {
			promptParts = append(promptParts, fmt.Sprintf("- %s", plugin))
		}
		promptParts = append(promptParts, "")
	}

	// =======================================================
	// PART 5: SYSTEM ANCHOR
	// =======================================================
	promptParts = append(promptParts, "### FINAL REMINDER ###")
	promptParts = append(promptParts, fmt.Sprintf("You are %s. Act accordingly.", agent.Name))

	if len(result.SkillsLoaded) > 0 {
		promptParts = append(promptParts, "Integrate the guidelines from all loaded skills into your reasoning.")
	}

	promptParts = append(promptParts, "If a user request requires specific knowledge listed in 'SKILL RESOURCES', use your tools to read those files FIRST.")

	result.FullPrompt = strings.Join(promptParts, "\n")
	return result, nil
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
