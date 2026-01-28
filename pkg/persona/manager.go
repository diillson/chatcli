/*
 * ChatCLI - Persona System
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package persona

import (
	"sync"

	"go.uber.org/zap"
)

// Manager handles the active persona state
type Manager struct {
	logger       *zap.Logger
	loader       *Loader
	builder      *Builder
	activeAgent  *Agent
	activePrompt *ComposedPrompt
	mu           sync.RWMutex
}

// NewManager creates a new persona manager
func NewManager(logger *zap.Logger) *Manager {
	loader := NewLoader(logger)
	return &Manager{
		logger:  logger,
		loader:  loader,
		builder: NewBuilder(logger, loader),
	}
}

// Initialize sets up the persona system (creates directories if needed)
func (m *Manager) Initialize() error {
	return m.loader.EnsureDirectories()
}

// SetProjectDir sets the project directory for local skills
func (m *Manager) SetProjectDir(dir string) {
	m.loader.SetProjectDir(dir)
}

// LoadAgent loads and activates an agent by name
func (m *Manager) LoadAgent(name string) (*LoadResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Build the composed prompt
	composed, err := m.builder.BuildSystemPrompt(name)
	if err != nil {
		return nil, err
	}

	// Get the agent for metadata
	agent, err := m.loader.GetAgent(name)
	if err != nil {
		return nil, err
	}

	// Set as active
	m.activeAgent = agent
	m.activePrompt = composed

	m.logger.Info("Agent loaded",
		zap.String("name", agent.Name),
		zap.Int("skills_loaded", len(composed.SkillsLoaded)),
		zap.Int("skills_missing", len(composed.SkillsMissing)))

	result := &LoadResult{
		Agent:         agent,
		LoadedSkills:  composed.SkillsLoaded,
		MissingSkills: composed.SkillsMissing,
	}

	return result, nil
}

// UnloadAgent deactivates the current agent
func (m *Manager) UnloadAgent() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.activeAgent != nil {
		m.logger.Info("Agent unloaded", zap.String("name", m.activeAgent.Name))
	}

	m.activeAgent = nil
	m.activePrompt = nil
}

// GetActiveAgent returns the currently active agent (nil if none)
func (m *Manager) GetActiveAgent() *Agent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeAgent
}

// GetActivePrompt returns the composed prompt for the active agent
func (m *Manager) GetActivePrompt() *ComposedPrompt {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activePrompt
}

// GetSystemPrompt returns the full system prompt string for the active agent
// Returns empty string if no agent is active
func (m *Manager) GetSystemPrompt() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.activePrompt == nil {
		return ""
	}
	return m.activePrompt.FullPrompt
}

// HasActiveAgent returns true if an agent is currently loaded
func (m *Manager) HasActiveAgent() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeAgent != nil
}

// ListAgents returns all available agents
func (m *Manager) ListAgents() ([]*Agent, error) {
	return m.loader.ListAgents()
}

// ListSkills returns all available skills
func (m *Manager) ListSkills() ([]*Skill, error) {
	return m.loader.ListSkills()
}

// GetAgentsDir returns the path to the agents directory
func (m *Manager) GetAgentsDir() string {
	return m.loader.GetAgentsDir()
}

// GetSkillsDir returns the path to the skills directory
func (m *Manager) GetSkillsDir() string {
	return m.loader.GetSkillsDir()
}

// ValidateAgent checks if an agent configuration is valid
func (m *Manager) ValidateAgent(name string) ([]string, []string, error) {
	return m.builder.ValidateAgent(name)
}
