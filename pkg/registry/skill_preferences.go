/*
 * ChatCLI - Skill Preferences
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Manages user preferences for which source to use when multiple skills
 * with the same base name are installed from different sources.
 *
 * Example: if "frontend-design" is installed both locally and from skills.sh,
 * the user can set a preference to use the skills.sh version:
 *
 *   /skill prefer frontend-design skills.sh
 *
 * Preferences are stored in ~/.chatcli/skill-preferences.yaml:
 *
 *   preferences:
 *     frontend-design: "skills.sh"
 *     code-review: "clawhub"
 */
package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// SkillPreferences manages preferred sources for skills with name conflicts.
type SkillPreferences struct {
	// Preferences maps base skill name → preferred source (e.g. "skills.sh", "local", "clawhub")
	Preferences map[string]string `yaml:"preferences"`
	filePath    string
	mu          sync.RWMutex
}

// PreferencesPath returns the path to the skill preferences file.
func PreferencesPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".chatcli", "skill-preferences.yaml")
}

// LoadPreferences reads skill preferences from disk.
// Returns an empty preferences object if the file doesn't exist.
func LoadPreferences() *SkillPreferences {
	sp := &SkillPreferences{
		Preferences: make(map[string]string),
		filePath:    PreferencesPath(),
	}

	data, err := os.ReadFile(sp.filePath)
	if err != nil {
		return sp // file doesn't exist yet — return empty prefs
	}

	if err := yaml.Unmarshal(data, sp); err != nil {
		return sp // corrupted — return empty prefs
	}

	if sp.Preferences == nil {
		sp.Preferences = make(map[string]string)
	}

	return sp
}

// Save writes current preferences to disk.
func (sp *SkillPreferences) Save() error {
	sp.mu.RLock()
	defer sp.mu.RUnlock()

	if err := os.MkdirAll(filepath.Dir(sp.filePath), 0o755); err != nil {
		return err
	}

	data, err := yaml.Marshal(sp)
	if err != nil {
		return err
	}

	return os.WriteFile(sp.filePath, data, 0o644)
}

// SetPreference sets the preferred source for a base skill name.
func (sp *SkillPreferences) SetPreference(baseName string, source string) error {
	sp.mu.Lock()
	sp.Preferences[baseName] = source
	sp.mu.Unlock()
	return sp.Save()
}

// RemovePreference removes a preference, returning to default resolution order.
func (sp *SkillPreferences) RemovePreference(baseName string) error {
	sp.mu.Lock()
	delete(sp.Preferences, baseName)
	sp.mu.Unlock()
	return sp.Save()
}

// GetPreference returns the preferred source for a base skill name.
// Returns empty string if no preference is set.
func (sp *SkillPreferences) GetPreference(baseName string) string {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	return sp.Preferences[baseName]
}

// ListPreferences returns all current preferences as a map.
func (sp *SkillPreferences) ListPreferences() map[string]string {
	sp.mu.RLock()
	defer sp.mu.RUnlock()

	result := make(map[string]string, len(sp.Preferences))
	for k, v := range sp.Preferences {
		result[k] = v
	}
	return result
}

// ResolvePreferred picks the preferred skill from a list of candidates sharing
// the same base name. Selection order:
//  1. If a preference is set for this base name, pick the matching source
//  2. Otherwise, return nil (let the caller use default priority)
func (sp *SkillPreferences) ResolvePreferred(baseName string, candidates []InstalledSkillInfo) *InstalledSkillInfo {
	preferred := sp.GetPreference(baseName)
	if preferred == "" {
		return nil // no preference, use default
	}

	for i, c := range candidates {
		if c.Source == preferred {
			return &candidates[i]
		}
	}

	return nil // preference set but no matching candidate found
}

// FormatPreferenceEntry returns a display-friendly string for a preference.
func FormatPreferenceEntry(baseName string, source string) string {
	return fmt.Sprintf("%s → %s", baseName, source)
}
