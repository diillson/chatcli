/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * skill_autoauthor.go — programmatic entry points into the @skill writer.
 *
 * The self-evolution engine (cli package) needs to create and evolve skills
 * WITHOUT going through the agent's tool-call path: it acts on candidates the
 * background extraction pass detected. Rather than re-implement the validated
 * write/render logic (slug validation, create-vs-update guard, YAML-safe
 * frontmatter), it calls these thin exported wrappers, which delegate to the
 * exact same writeSkill/renderSkill/resolveSkillsDir used by @skill. This keeps
 * a single source of truth for the on-disk skill format.
 */
package plugins

import (
	"os"
	"path/filepath"
)

// AutoSkillInput is the cross-package payload for programmatic skill authoring.
// It mirrors the @skill create/update fields the self-evolution engine needs;
// pack-only fields are intentionally omitted.
type AutoSkillInput struct {
	Name         string
	Description  string
	Content      string
	Triggers     []string
	AllowedTools []string
}

// SkillsDir returns the global skills directory the loader scans
// (~/.chatcli/skills, or the test override). The self-evolution engine uses it
// to locate its edit-safety manifest alongside the skills it authors.
func SkillsDir() (string, error) {
	return resolveSkillsDir()
}

// SaveSkill creates (update=false) or evolves (update=true) a skill, reusing
// the @skill writer. It enforces the same invariants: a valid slug, refusing
// to clobber an existing skill on create, and refusing to evolve a missing one
// on update. It returns the path of the SKILL.md written.
func SaveSkill(in AutoSkillInput, update bool) (string, error) {
	dir, err := resolveSkillsDir()
	if err != nil {
		return "", err
	}
	return writeSkill(dir, update, skillInput{
		Name:         in.Name,
		Description:  in.Description,
		Content:      in.Content,
		Triggers:     in.Triggers,
		AllowedTools: in.AllowedTools,
	})
}

// ReadSkillContent returns the raw SKILL.md for name and whether it exists.
// The self-evolution engine compares this against its manifest hash to decide
// whether a skill is still engine-owned (safe to evolve) or has been authored
// or hand-edited by the user (must not be clobbered).
func ReadSkillContent(name string) (string, bool) {
	if !skillNameRe.MatchString(name) {
		return "", false
	}
	dir, err := resolveSkillsDir()
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(dir, name, "SKILL.md")) // #nosec G304 -- name validated against slug regex
	if err != nil {
		return "", false
	}
	return string(data), true
}

// SkillExists reports whether a skill with the given name is saved.
func SkillExists(name string) bool {
	_, ok := ReadSkillContent(name)
	return ok
}

// skillBackupName is the one-time snapshot the self-evolution engine takes
// before it first modifies a user-authored skill, so the original is always
// recoverable via @skill restore.
const skillBackupName = "SKILL.md.bak"

func skillPaths(name string) (skillMD, backup string, err error) {
	if !skillNameRe.MatchString(name) {
		return "", "", os.ErrInvalid
	}
	dir, err := resolveSkillsDir()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, name, "SKILL.md"), filepath.Join(dir, name, skillBackupName), nil
}

// HasBackup reports whether a recoverable backup exists for name.
func HasBackup(name string) bool {
	_, backup, err := skillPaths(name)
	if err != nil {
		return false
	}
	_, statErr := os.Stat(backup)
	return statErr == nil
}

// BackupSkill snapshots the current SKILL.md to SKILL.md.bak, but only if no
// backup exists yet (the first engine modification is the one worth preserving;
// later ones would overwrite the user's original with engine output). Returns
// whether a backup was created.
func BackupSkill(name string) (bool, error) {
	skillMD, backup, err := skillPaths(name)
	if err != nil {
		return false, err
	}
	if _, statErr := os.Stat(backup); statErr == nil {
		return false, nil // already preserved
	}
	data, err := os.ReadFile(skillMD) // #nosec G304 -- name validated against slug regex
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(backup, data, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// RestoreSkill restores a skill from its backup and removes the backup, undoing
// the engine's modifications. Returns false (no error) when there is nothing to
// restore.
func RestoreSkill(name string) (bool, error) {
	skillMD, backup, err := skillPaths(name)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(backup) // #nosec G304 -- name validated against slug regex
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := os.WriteFile(skillMD, data, 0o600); err != nil {
		return false, err
	}
	return true, os.Remove(backup)
}

// SetSkillsDirOverride redirects the skills directory. Exported for the
// self-evolution engine's integration tests, which exercise authoring against
// a temp directory; production code never calls it.
func SetSkillsDirOverride(dir string) { skillsDirOverride = dir }
