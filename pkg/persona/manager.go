/*
 * ChatCLI - Persona System
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package persona

import (
	"fmt"
	"sort"
	"sync"

	"go.uber.org/zap"
)

// Manager handles the active persona state
type Manager struct {
	logger  *zap.Logger
	loader  *Loader
	builder *Builder

	// Changed from single activeAgent to activeAgents map
	activeAgents map[string]*Agent
	activePrompt *ComposedPrompt
	mu           sync.RWMutex
}

// NewManager creates a new persona manager
func NewManager(logger *zap.Logger) *Manager {
	loader := NewLoader(logger)
	return &Manager{
		logger:       logger,
		loader:       loader,
		builder:      NewBuilder(logger, loader),
		activeAgents: make(map[string]*Agent),
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

// LoadAgent (Legacy/Reset Mode): Clears all agents and loads specific one
func (m *Manager) LoadAgent(name string) (*LoadResult, error) {
	m.UnloadAllAgents() // Clear all first
	return m.AttachAgent(name)
}

// AttachAgent adds an agent to the active pool without removing others
func (m *Manager) AttachAgent(name string) (*LoadResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. Check if already attached
	if _, exists := m.activeAgents[name]; exists {
		return nil, fmt.Errorf("agent '%s' is already attached", name)
	}

	// 2. Load agent from disk
	agent, err := m.loader.GetAgent(name)
	if err != nil {
		return nil, err
	}

	// 3. Temporarily add to map to build prompt
	m.activeAgents[name] = agent

	// 4. Rebuild the composite prompt with all agents
	composed, err := m.rebuildPromptInternal()
	if err != nil {
		delete(m.activeAgents, name) // Rollback
		return nil, err
	}

	m.activePrompt = composed

	m.logger.Info("Agent attached", zap.String("name", name), zap.Int("total_active", len(m.activeAgents)))

	return &LoadResult{
		Agent:         agent,
		LoadedSkills:  composed.SkillsLoaded,
		MissingSkills: composed.SkillsMissing,
	}, nil
}

// DetachAgent removes a specific agent
func (m *Manager) DetachAgent(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.activeAgents[name]; !exists {
		return fmt.Errorf("agent '%s' is not active", name)
	}

	delete(m.activeAgents, name)

	// If no agents left, clean prompt
	if len(m.activeAgents) == 0 {
		m.activePrompt = nil
		return nil
	}

	// Rebuild prompt with remaining agents
	composed, err := m.rebuildPromptInternal()
	if err != nil {
		return fmt.Errorf("error rebuilding prompt after detach: %w", err)
	}
	m.activePrompt = composed

	m.logger.Info("Agent detached", zap.String("name", name))
	return nil
}

// UnloadAllAgents (antigo UnloadAgent) clears everything
func (m *Manager) UnloadAllAgents() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeAgents = make(map[string]*Agent)
	m.activePrompt = nil
	m.logger.Info("All agents unloaded")
}

// UnloadAgent (legacy) - alias for backward compatibility
func (m *Manager) UnloadAgent() {
	m.UnloadAllAgents()
}

// Helper internal (chame SEMPRE dentro de um Lock)
// Pega todos os agentes em m.activeAgents e chama o Builder
func (m *Manager) rebuildPromptInternal() (*ComposedPrompt, error) {
	// Convert map to slice for the builder
	var agents []*Agent
	for _, a := range m.activeAgents {
		agents = append(agents, a)
	}

	// Sort by name for deterministic prompts
	sort.Slice(agents, func(i, j int) bool {
		return agents[i].Name < agents[j].Name
	})

	return m.builder.BuildMultiAgentPrompt(agents)
}

// GetActiveAgents returns list of active agents
func (m *Manager) GetActiveAgents() []*Agent {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var list []*Agent
	for _, a := range m.activeAgents {
		list = append(list, a)
	}
	return list
}

// GetActiveAgent returns the first active agent (legacy compatibility)
func (m *Manager) GetActiveAgent() *Agent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, a := range m.activeAgents {
		return a
	}
	return nil
}

// GetActivePrompt returns the composed prompt for the active agent
func (m *Manager) GetActivePrompt() *ComposedPrompt {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activePrompt
}

// GetSystemPrompt returns the full system prompt string for the active agent
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
	return len(m.activeAgents) > 0
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

// GetLoader returns the underlying loader for advanced callers (e.g., the worker system).
func (m *Manager) GetLoader() *Loader {
	return m.loader
}

// GetSkill loads a skill by name, delegating to the loader.
func (m *Manager) GetSkill(name string) (*Skill, error) {
	return m.loader.GetSkill(name)
}

// ValidateAgent checks if an agent configuration is valid
func (m *Manager) ValidateAgent(name string) ([]string, []string, error) {
	return m.builder.ValidateAgent(name)
}
