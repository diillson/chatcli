/*
 * ChatCLI - Persona System
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package persona

import (
	"fmt"
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

// BuildSystemPrompt creates a composed system prompt from an agent and its skills
// Ordem de Montagem (clara e documentada):
//  1. [ROLE] - Identidade do Agente (você é...)
//  2. [PERSONALITY] - Conteúdo base do agente (markdown body)
//  3. [SKILLS] - Conhecimento especializado das skills
//  4. [PLUGINS] - Hints de plugins habilitados
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
	// SECTION 1: [ROLE] - Identidade do Agente
	// =======================================================
	promptParts = append(promptParts, "====================================================")
	promptParts = append(promptParts, fmt.Sprintf("[ROLE] AGENTE: %s", agent.Name))
	promptParts = append(promptParts, "====================================================")

	if agent.Description != "" {
		promptParts = append(promptParts, fmt.Sprintf("Descrição: %s", agent.Description))
	}
	promptParts = append(promptParts, "")

	// =======================================================
	// SECTION 2: [PERSONALITY] - Conteúdo Base do Agente
	// =======================================================
	if agent.Content != "" {
		promptParts = append(promptParts, "[PERSONALIDADE E COMPORTAMENTO]")
		promptParts = append(promptParts, strings.Repeat("-", 50))
		promptParts = append(promptParts, agent.Content)
		promptParts = append(promptParts, "")
	}

	// =======================================================
	// SECTION 3: [SKILLS] - Conhecimento Especializado
	// =======================================================
	if len(agent.Skills) > 0 {
		promptParts = append(promptParts, "[SKILLS / CONHECIMENTO ESPECIALIZADO]")
		promptParts = append(promptParts, "As seguintes regras e conhecimentos DEVEM ser aplicados:")
		promptParts = append(promptParts, strings.Repeat("-", 50))

		for i, skillName := range agent.Skills {
			skillName = strings.TrimSpace(skillName)
			if skillName == "" {
				continue
			}

			skill, err := b.loader.GetSkill(skillName)
			if err != nil {
				b.logger.Warn("Skill not found",
					zap.String("skill", skillName),
					zap.Error(err))
				result.SkillsMissing = append(result.SkillsMissing, skillName)
				continue
			}

			promptParts = append(promptParts, "")
			promptParts = append(promptParts, fmt.Sprintf("=★ Skill %d: %s ▅▅▅", i+1, skill.Name))
			if skill.Description != "" {
				promptParts = append(promptParts, fmt.Sprintf("   Propósito: %s", skill.Description))
			}
			promptParts = append(promptParts, skill.Content)
			result.SkillsLoaded = append(result.SkillsLoaded, skillName)
		}
		promptParts = append(promptParts, "")
	}

	// =======================================================
	// SECTION 4: [PLUGINS] - Hints de Plugins
	// =======================================================
	if len(agent.Plugins) > 0 {
		promptParts = append(promptParts, "[PLUGINS HABILITADOS]")
		promptParts = append(promptParts, "Você tem acesso aos seguintes plugins/perramentas nesta sessão:")
		for _, plugin := range agent.Plugins {
			promptParts = append(promptParts, fmt.Sprintf("  - %s", plugin))
		}
		promptParts = append(promptParts, "")
	}

	// =======================================================
	// FINAL: Anchor / Lembrete
	// =======================================================
	promptParts = append(promptParts, "[LEMBRETE FINAL]")
	promptParts = append(promptParts, fmt.Sprintf("Você está operando como o agente '%s'.", agent.Name))
	if len(result.SkillsLoaded) > 0 {
		promptParts = append(promptParts, fmt.Sprintf("Aplique SEMPRE as regras das skills: %s.", strings.Join(result.SkillsLoaded, ", ")))
	}
	promptParts = append(promptParts, "====================================================")

	result.FullPrompt = strings.Join(promptParts, "\n")
	return result, nil
}

// ValidateAgent checks if an agent and its skills are valid
func (b *Builder) ValidateAgent(agentName string) ([]string, []string, error) {
	agent, err := b.loader.GetAgent(agentName)
	if err != nil {
		return nil, nil, err
	}

	var warnings, errors []string

	// Check if content is empty
	if strings.TrimSpace(agent.Content) == "" {
		warnings = append(warnings, "Agent has no base content (empty body)")
	}

	// Check skills
	for _, skillName := range agent.Skills {
		skillName = strings.TrimSpace(skillName)
		if skillName == "" {
			continue
		}

		_, err := b.loader.GetSkill(skillName)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Skill '%s' not found", skillName))
		}
	}

	return warnings, errors, nil
}
