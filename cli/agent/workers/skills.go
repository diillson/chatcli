package workers

import (
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/pkg/coder/engine"
)

// SkillType distinguishes between executable scripts and descriptive skills.
type SkillType int

const (
	// SkillDescriptive is a skill that informs the LLM about capabilities;
	// the worker agent resolves it via its mini ReAct loop.
	SkillDescriptive SkillType = iota
	// SkillExecutable is a skill with a pre-built script that runs directly,
	// bypassing the LLM for mechanical operations.
	SkillExecutable
)

// SkillFunc is the signature for executable skill scripts.
// It receives context, input parameters, and a fresh Engine for tool execution.
type SkillFunc func(ctx context.Context, input map[string]string, eng *engine.Engine) (string, error)

// Skill represents a pre-built compound operation (macro) available to an agent.
type Skill struct {
	Name        string
	Description string
	Type        SkillType
	// Script is the executable function for SkillExecutable skills.
	// Nil for SkillDescriptive skills.
	Script SkillFunc
}

// SkillSet is a collection of skills for a single agent.
type SkillSet struct {
	skills map[string]*Skill
}

// NewSkillSet creates an empty SkillSet.
func NewSkillSet() *SkillSet {
	return &SkillSet{
		skills: make(map[string]*Skill),
	}
}

// Register adds a skill to the set.
func (s *SkillSet) Register(skill *Skill) {
	s.skills[skill.Name] = skill
}

// Get looks up a skill by name.
func (s *SkillSet) Get(name string) (*Skill, bool) {
	sk, ok := s.skills[name]
	return sk, ok
}

// List returns all registered skills.
func (s *SkillSet) List() []*Skill {
	result := make([]*Skill, 0, len(s.skills))
	for _, sk := range s.skills {
		result = append(result, sk)
	}
	return result
}

// Execute runs an executable skill directly, bypassing the LLM.
// Returns an error if the skill is not found or is not executable.
func (s *SkillSet) Execute(ctx context.Context, name string, input map[string]string, eng *engine.Engine) (string, error) {
	sk, ok := s.skills[name]
	if !ok {
		return "", fmt.Errorf("skill not found: %s", name)
	}
	if sk.Type != SkillExecutable || sk.Script == nil {
		return "", fmt.Errorf("skill %s is not executable", name)
	}
	return sk.Script(ctx, input, eng)
}

// CatalogString generates a skill listing for system prompts.
func (s *SkillSet) CatalogString() string {
	if len(s.skills) == 0 {
		return ""
	}
	var b strings.Builder
	for _, sk := range s.skills {
		tag := "descriptive"
		if sk.Type == SkillExecutable {
			tag = "script"
		}
		fmt.Fprintf(&b, "  - %s [%s]: %s\n", sk.Name, tag, sk.Description)
	}
	return b.String()
}
