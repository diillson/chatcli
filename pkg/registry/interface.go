/*
 * ChatCLI - Skill Registry Interface
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package registry

import (
	"context"
	"time"
)

// ModerationFlags represents safety signals from a registry about a skill.
type ModerationFlags struct {
	MalwareDetected   bool   `json:"malware_detected"`
	SuspiciousContent bool   `json:"suspicious_content"`
	Quarantined       bool   `json:"quarantined"`
	Reason            string `json:"reason,omitempty"`
}

// SkillMeta is the metadata returned by a registry about a skill.
type SkillMeta struct {
	Name         string          `json:"name"`
	Slug         string          `json:"slug"`
	Description  string          `json:"description"`
	Version      string          `json:"version"`
	Author       string          `json:"author"`
	Tags         []string        `json:"tags"`
	Downloads    int             `json:"downloads"`
	DownloadURL  string          `json:"download_url"`
	RegistryName string          `json:"registry_name"`
	Moderation   ModerationFlags `json:"moderation"`
}

// SearchResult wraps search results from a single registry.
type SearchResult struct {
	RegistryName string
	Skills       []SkillMeta
	Error        error
}

// InstallResult reports what happened during installation.
type InstallResult struct {
	Name         string
	Version      string
	InstallPath  string
	Source       string
	Moderation   ModerationFlags
	WasDuplicate bool
}

// SnapshotFile represents a single file in a skills.sh download snapshot.
type SnapshotFile struct {
	Path     string // relative path, e.g. "SKILL.md", "scripts/deploy.sh"
	Contents string // file contents
}

// InstalledSkillInfo describes a locally installed skill.
type InstalledSkillInfo struct {
	Name        string
	BaseName    string // skill name without source prefix (e.g. "frontend-design")
	Description string
	Version     string
	Source      string // registry name or "local"
	Path        string
}

// RegistryInfo describes a configured registry.
type RegistryInfo struct {
	Name          string
	URL           string
	Enabled       bool
	TempDisabled  bool       // auto-disabled due to consecutive failures
	DisabledUntil *time.Time // when the cooldown expires
	FailureCount  int
}

// SkillRegistry is the interface each registry adapter must implement.
type SkillRegistry interface {
	// Name returns the human-readable name of this registry.
	Name() string

	// Search returns skills matching the query string.
	Search(ctx context.Context, query string) ([]SkillMeta, error)

	// GetSkillMeta returns full metadata for a specific skill by name/slug.
	GetSkillMeta(ctx context.Context, nameOrSlug string) (*SkillMeta, error)

	// DownloadSkill downloads the skill content and returns it as bytes.
	DownloadSkill(ctx context.Context, meta *SkillMeta) ([]byte, error)

	// Enabled returns whether this registry is currently active.
	Enabled() bool
}
