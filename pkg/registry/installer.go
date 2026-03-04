/*
 * ChatCLI - Skill Installer
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Installer handles downloading, verifying, and writing skills to disk.
type Installer struct {
	installDir string
	logger     *zap.Logger
}

// NewInstaller creates a new skill installer.
func NewInstaller(installDir string, logger *zap.Logger) *Installer {
	return &Installer{
		installDir: installDir,
		logger:     logger,
	}
}

// Install writes skill content atomically to the install directory.
// It creates a V2 skill package (directory with SKILL.md).
func (inst *Installer) Install(meta *SkillMeta, content []byte) (*InstallResult, error) {
	// Check moderation — hard block on malware/quarantine
	if ShouldBlock(meta.Moderation) {
		return nil, fmt.Errorf("%s", CheckModeration(meta))
	}

	// Ensure install directory exists
	if err := os.MkdirAll(inst.installDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating install directory: %w", err)
	}

	skillName := sanitizeName(meta.Name)
	finalDir := filepath.Join(inst.installDir, skillName)

	// Check for duplicate
	wasDuplicate := false
	if _, err := os.Stat(finalDir); err == nil {
		wasDuplicate = true
		// Remove old version for update
		if err := os.RemoveAll(finalDir); err != nil {
			return nil, fmt.Errorf("removing existing skill: %w", err)
		}
	}

	// Write to temp directory first (atomic)
	tmpDir := filepath.Join(inst.installDir, fmt.Sprintf(".tmp-%s-%s", skillName, uuid.New().String()[:8]))
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir) // cleanup temp on failure

	// Build SKILL.md with frontmatter
	skillContent := buildSkillMD(meta, content)
	skillFile := filepath.Join(tmpDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte(skillContent), 0o644); err != nil {
		return nil, fmt.Errorf("writing SKILL.md: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpDir, finalDir); err != nil {
		return nil, fmt.Errorf("installing skill (rename): %w", err)
	}

	inst.logger.Info("skill installed",
		zap.String("name", skillName),
		zap.String("version", meta.Version),
		zap.String("source", meta.RegistryName),
		zap.String("path", finalDir),
	)

	return &InstallResult{
		Name:         skillName,
		Version:      meta.Version,
		InstallPath:  finalDir,
		Source:       meta.RegistryName,
		Moderation:   meta.Moderation,
		WasDuplicate: wasDuplicate,
	}, nil
}

// Uninstall removes an installed skill from disk.
func (inst *Installer) Uninstall(name string) error {
	skillName := sanitizeName(name)
	skillDir := filepath.Join(inst.installDir, skillName)

	if _, err := os.Stat(skillDir); os.IsNotExist(err) {
		// Also check for single-file skill
		skillFile := filepath.Join(inst.installDir, skillName+".md")
		if _, err := os.Stat(skillFile); os.IsNotExist(err) {
			return fmt.Errorf("skill '%s' not installed", name)
		}
		return os.Remove(skillFile)
	}

	return os.RemoveAll(skillDir)
}

// IsInstalled checks if a skill exists locally.
func (inst *Installer) IsInstalled(name string) bool {
	skillName := sanitizeName(name)
	skillDir := filepath.Join(inst.installDir, skillName)
	if _, err := os.Stat(skillDir); err == nil {
		return true
	}
	// Check single-file
	skillFile := filepath.Join(inst.installDir, skillName+".md")
	_, err := os.Stat(skillFile)
	return err == nil
}

// ListInstalled returns all installed skills with their metadata.
func (inst *Installer) ListInstalled() ([]InstalledSkillInfo, error) {
	entries, err := os.ReadDir(inst.installDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var skills []InstalledSkillInfo
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue // skip hidden/temp dirs
		}

		if entry.IsDir() {
			skillFile := filepath.Join(inst.installDir, entry.Name(), "SKILL.md")
			info := InstalledSkillInfo{
				Name: entry.Name(),
				Path: filepath.Join(inst.installDir, entry.Name()),
			}
			// Try to read metadata from SKILL.md frontmatter
			if data, err := os.ReadFile(skillFile); err == nil {
				parseInstalledMeta(&info, string(data))
			}
			skills = append(skills, info)
		} else if strings.HasSuffix(entry.Name(), ".md") {
			skills = append(skills, InstalledSkillInfo{
				Name:   strings.TrimSuffix(entry.Name(), ".md"),
				Source: "local",
				Path:   filepath.Join(inst.installDir, entry.Name()),
			})
		}
	}

	return skills, nil
}

// GetInstallDir returns the install directory path.
func (inst *Installer) GetInstallDir() string {
	return inst.installDir
}

// buildSkillMD generates SKILL.md content with YAML frontmatter.
func buildSkillMD(meta *SkillMeta, content []byte) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %q\n", meta.Name))
	if meta.Description != "" {
		sb.WriteString(fmt.Sprintf("description: %q\n", meta.Description))
	}
	if meta.Version != "" {
		sb.WriteString(fmt.Sprintf("version: %q\n", meta.Version))
	}
	if meta.Author != "" {
		sb.WriteString(fmt.Sprintf("author: %q\n", meta.Author))
	}
	if meta.RegistryName != "" {
		sb.WriteString(fmt.Sprintf("source: %q\n", meta.RegistryName))
	}
	sb.WriteString("---\n\n")
	sb.Write(content)
	return sb.String()
}

// parseInstalledMeta extracts metadata from SKILL.md frontmatter.
func parseInstalledMeta(info *InstalledSkillInfo, content string) {
	lines := strings.Split(content, "\n")
	inFrontmatter := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			}
			break // end of frontmatter
		}
		if !inFrontmatter {
			continue
		}

		if strings.HasPrefix(trimmed, "description:") {
			info.Description = unquote(strings.TrimPrefix(trimmed, "description:"))
		} else if strings.HasPrefix(trimmed, "version:") {
			info.Version = unquote(strings.TrimPrefix(trimmed, "version:"))
		} else if strings.HasPrefix(trimmed, "source:") {
			info.Source = unquote(strings.TrimPrefix(trimmed, "source:"))
		}
	}

	if info.Source == "" {
		info.Source = "local"
	}
}

// unquote removes surrounding quotes and trims whitespace.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'")
	return s
}

// sanitizeName cleans a skill name for use as a directory name.
func sanitizeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "..", "")
	return name
}
