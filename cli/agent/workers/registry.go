package workers

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/diillson/chatcli/pkg/persona"
	"go.uber.org/zap"
)

// Registry holds all registered specialized agents.
type Registry struct {
	agents map[AgentType]WorkerAgent
	mu     sync.RWMutex
}

// NewRegistry creates an empty agent registry.
func NewRegistry() *Registry {
	return &Registry{
		agents: make(map[AgentType]WorkerAgent),
	}
}

// Register adds a specialized agent to the registry.
func (r *Registry) Register(a WorkerAgent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[a.Type()] = a
}

// Get returns a registered agent by type.
func (r *Registry) Get(agentType AgentType) (WorkerAgent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[agentType]
	return a, ok
}

// All returns all registered agents sorted by type name.
func (r *Registry) All() []WorkerAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	list := make([]WorkerAgent, 0, len(r.agents))
	for _, a := range r.agents {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool {
		return string(list[i].Type()) < string(list[j].Type())
	})
	return list
}

// Unregister removes a specialized agent from the registry.
// This allows users to replace built-in agents with custom implementations.
func (r *Registry) Unregister(agentType AgentType) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, agentType)
}

// CatalogString generates the agent catalog text for the orchestrator's system prompt.
// The catalog describes each agent's expertise and available skills so the LLM
// can make informed routing decisions.
func (r *Registry) CatalogString() string {
	// Collect agents under lock, then format outside lock
	agents := r.All()

	if len(agents) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Available Specialized Agents\n\n")

	for _, a := range agents {
		fmt.Fprintf(&b, "### %s (%s)\n", a.Type(), a.Name())
		fmt.Fprintf(&b, "%s\n", a.Description())
		if a.IsReadOnly() {
			b.WriteString("Access: READ-ONLY (cannot modify files)\n")
		}
		fmt.Fprintf(&b, "Allowed commands: %s\n", strings.Join(a.AllowedCommands(), ", "))
		skills := a.Skills()
		if skills != nil {
			catalog := skills.CatalogString()
			if catalog != "" {
				b.WriteString("Skills:\n")
				b.WriteString(catalog)
			}
		}
		b.WriteString("\n")
	}

	return b.String()
}

// SetupDefaultRegistry creates a registry with all default specialized agents.
func SetupDefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(NewFileAgent())
	r.Register(NewCoderAgent())
	r.Register(NewShellAgent())
	r.Register(NewGitAgent())
	r.Register(NewSearchAgent())
	r.Register(NewPlannerAgent())
	r.Register(NewReviewerAgent())
	r.Register(NewTesterAgent())
	r.Register(NewRefactorAgent())
	r.Register(NewDiagnosticsAgent())
	r.Register(NewFormatterAgent())
	r.Register(NewDepsAgent())
	return r
}

// LoadCustomAgents scans the persona system for custom agents and registers them
// as CustomAgent workers in the registry. Built-in agent types are protected from
// override — if a custom agent name collides, it is skipped with a warning.
// Returns the number of agents successfully loaded.
func LoadCustomAgents(registry *Registry, mgr *persona.Manager, logger *zap.Logger) int {
	agents, err := mgr.ListAgents()
	if err != nil {
		logger.Warn("Failed to list persona agents for worker registration", zap.Error(err))
		return 0
	}

	loaded := 0
	for _, pa := range agents {
		agentType := AgentType(strings.ToLower(pa.Name))

		// Protect built-in agent types from override
		if builtinAgentTypes[agentType] {
			logger.Warn("Custom agent name conflicts with built-in agent, skipping",
				zap.String("agent", pa.Name),
				zap.String("type", string(agentType)),
			)
			continue
		}

		// Skip duplicates
		if _, exists := registry.Get(agentType); exists {
			logger.Warn("Duplicate custom agent name, skipping",
				zap.String("agent", pa.Name),
			)
			continue
		}

		// Load persona skills for this agent
		var personaSkills []*persona.Skill
		for _, skillName := range pa.Skills {
			skillName = strings.TrimSpace(skillName)
			if skillName == "" {
				continue
			}
			skill, err := mgr.GetSkill(skillName)
			if err != nil {
				logger.Warn("Skill not found for custom agent, skipping skill",
					zap.String("agent", pa.Name),
					zap.String("skill", skillName),
					zap.Error(err),
				)
				continue
			}
			personaSkills = append(personaSkills, skill)
		}

		customAgent := NewCustomAgent(pa, personaSkills)
		registry.Register(customAgent)
		loaded++

		logger.Info("Registered custom persona agent as worker",
			zap.String("agent", pa.Name),
			zap.String("type", string(agentType)),
			zap.Int("commands", len(customAgent.AllowedCommands())),
			zap.Int("skills", len(personaSkills)),
			zap.Bool("readOnly", customAgent.IsReadOnly()),
		)
	}

	return loaded
}
