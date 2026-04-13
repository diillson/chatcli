/*
 * ChatCLI - Skill Installer
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
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
	if err := os.MkdirAll(inst.installDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating install directory: %w", err)
	}

	// Use qualified name for registry installs to avoid collisions
	skillName := qualifiedInstallName(meta)
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
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir) // cleanup temp on failure

	// Build SKILL.md with frontmatter
	skillContent := buildSkillMD(meta, content)
	skillFile := filepath.Join(tmpDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte(skillContent), 0o600); err != nil {
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

// InstallFromSnapshot writes a skill from a skills.sh snapshot atomically to disk.
// Unlike Install(), this preserves the original SKILL.md and all supporting files
// (scripts/, references/, assets/) exactly as provided by the upstream source.
// The snapshot contains pre-built files with a content hash for integrity.
func (inst *Installer) InstallFromSnapshot(meta *SkillMeta, files []SnapshotFile, snapshotHash string) (*InstallResult, error) {
	// Check moderation — hard block on malware/quarantine
	if ShouldBlock(meta.Moderation) {
		return nil, fmt.Errorf("%s", CheckModeration(meta))
	}

	// Ensure install directory exists
	if err := os.MkdirAll(inst.installDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating install directory: %w", err)
	}

	// Use qualified name to avoid collisions between registries
	skillName := qualifiedInstallName(meta)
	finalDir := filepath.Join(inst.installDir, skillName)

	// Check for duplicate — only from the SAME qualified name (same source + skill)
	wasDuplicate := false
	if _, err := os.Stat(finalDir); err == nil {
		wasDuplicate = true
		if err := os.RemoveAll(finalDir); err != nil {
			return nil, fmt.Errorf("removing existing skill: %w", err)
		}
	}

	// Write to temp directory first (atomic)
	tmpDir := filepath.Join(inst.installDir, fmt.Sprintf(".tmp-%s-%s", skillName, uuid.New().String()[:8]))
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir) // cleanup temp on failure

	// Write each file from the snapshot
	for _, f := range files {
		if f.Path == "" {
			continue
		}
		// Path already validated by ParseSnapshotFiles (rejects ".." and absolute paths)
		targetPath := filepath.Clean(filepath.Join(tmpDir, filepath.FromSlash(f.Path))) // #nosec G304 -- path validated upstream

		// Create parent directories as needed
		parentDir := filepath.Dir(targetPath)
		if err := os.MkdirAll(parentDir, 0o750); err != nil {
			return nil, fmt.Errorf("creating directory for %s: %w", f.Path, err)
		}

		// Determine file permissions: scripts get executable bit
		perm := os.FileMode(0o600)
		if isScriptFile(f.Path) {
			perm = 0o750
		}

		if err := os.WriteFile(targetPath, []byte(f.Contents), perm); err != nil { // #nosec G306 -- perm set by isScriptFile
			return nil, fmt.Errorf("writing %s: %w", f.Path, err)
		}
	}

	// Inject source metadata into SKILL.md frontmatter if not already present,
	// so we can track provenance on the installed skill.
	skillMDPath := filepath.Clean(filepath.Join(tmpDir, "SKILL.md"))
	if data, err := os.ReadFile(skillMDPath); err == nil { // #nosec G304 -- path is constructed from sanitized skill name
		patched := injectSourceField(string(data), meta.RegistryName, snapshotHash)
		if patched != string(data) {
			_ = os.WriteFile(skillMDPath, []byte(patched), 0o600) // #nosec G703 -- path constructed from sanitized name + tmpDir
		}
	}

	// Atomic rename
	if err := os.Rename(tmpDir, finalDir); err != nil {
		return nil, fmt.Errorf("installing skill (rename): %w", err)
	}

	// Extract version from installed SKILL.md
	version := meta.Version
	if version == "" {
		info := InstalledSkillInfo{Name: skillName}
		if data, err := os.ReadFile(filepath.Clean(filepath.Join(finalDir, "SKILL.md"))); err == nil { // #nosec G304 -- path from sanitized installDir
			parseInstalledMeta(&info, string(data))
			version = info.Version
		}
	}

	inst.logger.Info("skill installed from snapshot",
		zap.String("name", skillName),
		zap.String("version", version),
		zap.String("source", meta.RegistryName),
		zap.Int("files", len(files)),
		zap.String("hash", snapshotHash),
		zap.String("path", finalDir),
	)

	return &InstallResult{
		Name:         skillName,
		Version:      version,
		InstallPath:  finalDir,
		Source:       meta.RegistryName,
		Moderation:   meta.Moderation,
		WasDuplicate: wasDuplicate,
	}, nil
}

// isScriptFile checks if a file path is in a scripts directory or has an executable extension.
func isScriptFile(filePath string) bool {
	normalized := filepath.ToSlash(filePath)
	if strings.HasPrefix(normalized, "scripts/") {
		return true
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	return ext == ".sh" || ext == ".py" || ext == ".rb" || ext == ".pl"
}

// injectSourceField adds a "source" and "snapshot_hash" field to SKILL.md
// frontmatter if they don't already exist. This preserves the original content
// while adding provenance tracking for installed skills.
func injectSourceField(content string, source string, hash string) string {
	lines := strings.Split(content, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		// No frontmatter — prepend minimal frontmatter with source
		var sb strings.Builder
		sb.WriteString("---\n")
		sb.WriteString(fmt.Sprintf("source: %q\n", source))
		if hash != "" {
			sb.WriteString(fmt.Sprintf("snapshot_hash: %q\n", hash))
		}
		sb.WriteString("---\n")
		sb.WriteString(content)
		return sb.String()
	}

	// Has frontmatter — check if source already exists
	hasSource := false
	hasHash := false
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "---" {
			endIdx = i
			break
		}
		if strings.HasPrefix(trimmed, "source:") {
			hasSource = true
		}
		if strings.HasPrefix(trimmed, "snapshot_hash:") {
			hasHash = true
		}
	}

	if endIdx < 0 {
		return content // malformed frontmatter, don't touch
	}

	// Insert missing fields before the closing ---
	var additions []string
	if !hasSource && source != "" {
		additions = append(additions, fmt.Sprintf("source: %q", source))
	}
	if !hasHash && hash != "" {
		additions = append(additions, fmt.Sprintf("snapshot_hash: %q", hash))
	}

	if len(additions) == 0 {
		return content // nothing to add
	}

	// Rebuild with additions inserted before closing ---
	var sb strings.Builder
	for i := 0; i < endIdx; i++ {
		sb.WriteString(lines[i])
		sb.WriteString("\n")
	}
	for _, add := range additions {
		sb.WriteString(add)
		sb.WriteString("\n")
	}
	for i := endIdx; i < len(lines); i++ {
		sb.WriteString(lines[i])
		if i < len(lines)-1 {
			sb.WriteString("\n")
		}
	}

	return sb.String()
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

// IsInstalled checks if a skill exists locally by exact directory name.
func (inst *Installer) IsInstalled(name string) bool {
	skillName := sanitizeName(name)
	if skillName == "" {
		return false
	}
	skillDir := filepath.Join(inst.installDir, skillName)
	if fi, err := os.Stat(skillDir); err == nil && fi.IsDir() {
		return true
	}
	// Check single-file
	skillFile := filepath.Join(inst.installDir, skillName+".md")
	_, err := os.Stat(skillFile)
	return err == nil
}

// IsInstalledFromSource checks if a skill with the given base name is installed
// from a specific registry source. This avoids false positives when a local
// "frontend-design" exists but we're checking for the skills.sh version.
func (inst *Installer) IsInstalledFromSource(baseName string, registryName string) bool {
	matches := inst.FindInstalled(baseName)
	for _, m := range matches {
		if m.Source == registryName {
			return true
		}
	}
	return false
}

// FindInstalled returns all installed skills that match a base name.
// This finds both unqualified (local) and qualified (registry) installs.
// e.g. searching "frontend-design" finds:
//   - "frontend-design" (local)
//   - "anthropics-skills--frontend-design" (skills.sh)
//   - "clawhub--frontend-design" (clawhub)
func (inst *Installer) FindInstalled(baseName string) []InstalledSkillInfo {
	baseName = sanitizeName(baseName)
	if baseName == "" {
		return nil
	}

	entries, err := os.ReadDir(inst.installDir)
	if err != nil {
		return nil
	}

	var matches []InstalledSkillInfo
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		dirName := entry.Name()
		_, entryBase := parseQualifiedName(dirName)

		// Match: exact name, or base name matches after stripping prefix
		if dirName != baseName && entryBase != baseName {
			continue
		}

		if entry.IsDir() {
			info := InstalledSkillInfo{
				Name:     dirName,
				BaseName: entryBase,
				Path:     filepath.Join(inst.installDir, dirName),
			}
			skillFile := filepath.Clean(filepath.Join(inst.installDir, dirName, "SKILL.md"))
			if data, err := os.ReadFile(skillFile); err == nil { // #nosec G304 -- installDir + dir entry name
				parseInstalledMeta(&info, string(data))
			}
			if info.Source == "" {
				info.Source = "local"
			}
			matches = append(matches, info)
		} else if strings.HasSuffix(dirName, ".md") {
			plainName := strings.TrimSuffix(dirName, ".md")
			if plainName == baseName {
				matches = append(matches, InstalledSkillInfo{
					Name:     plainName,
					BaseName: plainName,
					Source:   "local",
					Path:     filepath.Join(inst.installDir, dirName),
				})
			}
		}
	}

	return matches
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
			skillFile := filepath.Clean(filepath.Join(inst.installDir, entry.Name(), "SKILL.md"))
			_, baseName := parseQualifiedName(entry.Name())
			info := InstalledSkillInfo{
				Name:     entry.Name(),
				BaseName: baseName,
				Path:     filepath.Join(inst.installDir, entry.Name()),
			}
			// Try to read metadata from SKILL.md frontmatter
			if data, err := os.ReadFile(skillFile); err == nil { // #nosec G304 -- installDir + directory entry
				parseInstalledMeta(&info, string(data))
			}
			skills = append(skills, info)
		} else if strings.HasSuffix(entry.Name(), ".md") {
			fileName := strings.TrimSuffix(entry.Name(), ".md")
			skills = append(skills, InstalledSkillInfo{
				Name:     fileName,
				BaseName: fileName,
				Source:   "local",
				Path:     filepath.Join(inst.installDir, entry.Name()),
			})
		}
	}

	return skills, nil
}

// GetInstalledInfo returns metadata for a specific installed skill, or nil if not found.
// Checks exact name first, then searches by base name.
func (inst *Installer) GetInstalledInfo(name string) *InstalledSkillInfo {
	skillName := sanitizeName(name)
	if skillName == "" {
		return nil
	}

	// Check exact directory match first
	skillDir := filepath.Join(inst.installDir, skillName)
	if fi, err := os.Stat(skillDir); err == nil && fi.IsDir() {
		_, baseName := parseQualifiedName(skillName)
		info := InstalledSkillInfo{
			Name:     skillName,
			BaseName: baseName,
			Path:     skillDir,
		}
		if data, err := os.ReadFile(filepath.Clean(filepath.Join(skillDir, "SKILL.md"))); err == nil { // #nosec G304 -- skillDir from sanitized name
			parseInstalledMeta(&info, string(data))
		}
		if info.Source == "" {
			info.Source = "local"
		}
		return &info
	}

	// Check single-file skill
	skillFile := filepath.Join(inst.installDir, skillName+".md")
	if _, err := os.Stat(skillFile); err == nil {
		info := InstalledSkillInfo{
			Name:     skillName,
			BaseName: skillName,
			Source:   "local",
			Path:     skillFile,
		}
		return &info
	}

	// Fallback: search by base name — find first match
	matches := inst.FindInstalled(skillName)
	if len(matches) > 0 {
		return &matches[0]
	}

	return nil
}

// GetAllInstalledInfo returns all installed skills matching a name (by base name).
// Unlike GetInstalledInfo which returns the first match, this returns ALL matches
// to support showing conflicts between local and registry installs.
func (inst *Installer) GetAllInstalledInfo(name string) []InstalledSkillInfo {
	return inst.FindInstalled(sanitizeName(name))
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

// qualifiedSeparator is the delimiter between source and skill name in
// qualified directory names. Double-dash is chosen because it's:
// - Filesystem safe on all platforms
// - Not valid in skill names (agentskills.io spec forbids consecutive hyphens)
// - Easy to split unambiguously
const qualifiedSeparator = "--"

// qualifiedInstallName returns the directory name for a skill from a registry.
// Skills from registries get a qualified name to avoid collisions:
//   - skills.sh slug "anthropics/skills/frontend-design" → "anthropics-skills--frontend-design"
//   - clawhub slug "frontend-design" from registry "clawhub" → "clawhub--frontend-design"
//   - local skills (source "local" or "") use the plain name → "frontend-design"
func qualifiedInstallName(meta *SkillMeta) string {
	baseName := sanitizeName(meta.Name)

	// Local or unknown source — no prefix
	if meta.RegistryName == "" || meta.RegistryName == "local" {
		return baseName
	}

	// For skills.sh: use the slug which includes owner/repo
	// "anthropics/skills/frontend-design" → prefix "anthropics-skills"
	if IsSkillsShSource(meta.RegistryName) && meta.Slug != "" {
		parts := strings.Split(meta.Slug, "/")
		if len(parts) >= 3 {
			// owner/repo/skill → "owner-repo--skill"
			prefix := sanitizeName(strings.Join(parts[:len(parts)-1], "/"))
			return prefix + qualifiedSeparator + baseName
		}
	}

	// For other registries: use registry name as prefix
	prefix := sanitizeName(meta.RegistryName)
	return prefix + qualifiedSeparator + baseName
}

// parseQualifiedName splits a qualified directory name into source prefix and base name.
// "anthropics-skills--frontend-design" → ("anthropics-skills", "frontend-design")
// "frontend-design"                    → ("", "frontend-design")
func parseQualifiedName(dirName string) (prefix string, baseName string) {
	idx := strings.LastIndex(dirName, qualifiedSeparator)
	if idx <= 0 {
		return "", dirName
	}
	return dirName[:idx], dirName[idx+len(qualifiedSeparator):]
}
