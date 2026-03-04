package skills

import "time"

// SkillSource indicates where a skill was loaded from.
type SkillSource int

const (
	SourceWorkspace SkillSource = iota // ./skills/{name}/SKILL.md
	SourceGlobal                       // ~/.chatcli/skills/{name}/SKILL.md
	SourceBuiltin                      // embedded/bundled skills
	SourceRemote                       // downloaded from registry
)

// String returns the source name.
func (s SkillSource) String() string {
	switch s {
	case SourceWorkspace:
		return "workspace"
	case SourceGlobal:
		return "global"
	case SourceBuiltin:
		return "builtin"
	case SourceRemote:
		return "remote"
	default:
		return "unknown"
	}
}

// Skill represents a loadable skill definition.
type Skill struct {
	Name        string      `json:"name" yaml:"name"`
	Description string      `json:"description" yaml:"description"`
	Version     string      `json:"version,omitempty" yaml:"version,omitempty"`
	Author      string      `json:"author,omitempty" yaml:"author,omitempty"`
	Tags        []string    `json:"tags,omitempty" yaml:"tags,omitempty"`
	Path        string      `json:"-" yaml:"-"`
	Source      SkillSource `json:"-" yaml:"-"`
	LoadedAt    time.Time   `json:"-" yaml:"-"`
}
