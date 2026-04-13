/*
 * ChatCLI - Registry Manager
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Coordinates fan-out parallel search across multiple skill registries.
 * Auto-disables registries after consecutive failures with cooldown.
 */
package registry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	defaultMaxFailures      = 3
	defaultCooldownDuration = 5 * time.Minute
)

// RegistryManager coordinates multiple registries for fan-out search.
type RegistryManager struct {
	registries       []SkillRegistry
	installer        *Installer
	searchCache      *TrigramCache
	config           RegistriesConfig
	maxConcurrent    int
	maxFailures      int
	cooldownDuration time.Duration
	failureCounts    map[string]int
	disabledUntil    map[string]time.Time
	logger           *zap.Logger
	mu               sync.RWMutex
}

// NewRegistryManager creates a new registry manager from configuration.
func NewRegistryManager(cfg RegistriesConfig, logger *zap.Logger) (*RegistryManager, error) {
	installer := NewInstaller(cfg.InstallDir, logger)

	cacheTTL := 5 * time.Minute
	rm := &RegistryManager{
		installer:        installer,
		searchCache:      NewTrigramCache(cfg.SearchCacheSize, cacheTTL),
		config:           cfg,
		maxConcurrent:    cfg.MaxConcurrent,
		maxFailures:      defaultMaxFailures,
		cooldownDuration: defaultCooldownDuration,
		failureCounts:    make(map[string]int),
		disabledUntil:    make(map[string]time.Time),
		logger:           logger,
	}

	if rm.maxConcurrent <= 0 {
		rm.maxConcurrent = 3
	}

	// Create registry adapters
	for _, entry := range cfg.Registries {
		reg := createRegistryAdapter(entry, logger)
		if reg != nil {
			rm.registries = append(rm.registries, reg)
		}
	}

	logger.Info("registry manager initialized",
		zap.Int("registries", len(rm.registries)),
		zap.String("install_dir", cfg.InstallDir),
	)

	return rm, nil
}

// createRegistryAdapter creates the appropriate adapter for a registry entry.
func createRegistryAdapter(entry RegistryEntry, logger *zap.Logger) SkillRegistry {
	switch entry.Type {
	case "clawhub":
		return NewClawHubRegistry(entry, logger)
	case "chatcli":
		return NewChatCLIRegistry(entry, logger)
	case "skillssh":
		return NewSkillsShRegistry(entry, logger)
	default:
		return NewCustomRegistry(entry, logger)
	}
}

// SearchAll performs a fan-out parallel search across all enabled registries.
// Results are merged and deduplicated by skill name (first registry wins).
func (rm *RegistryManager) SearchAll(ctx context.Context, query string) ([]SkillMeta, []SearchResult) {
	// 1. Check trigram cache first
	if cached := rm.searchCache.Get(query); cached != nil {
		rm.logger.Debug("search cache hit", zap.String("query", query))
		return cached, nil
	}

	rm.mu.RLock()
	regs := rm.enabledRegistries()
	rm.mu.RUnlock()

	if len(regs) == 0 {
		return nil, nil
	}

	// 2. Fan-out with semaphore
	sem := make(chan struct{}, rm.maxConcurrent)
	results := make([]SearchResult, len(regs))
	var wg sync.WaitGroup

	for i, reg := range regs {
		wg.Add(1)
		go func(idx int, r SkillRegistry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			skills, err := r.Search(ctx, query)
			results[idx] = SearchResult{
				RegistryName: r.Name(),
				Skills:       skills,
				Error:        err,
			}
		}(i, reg)
	}
	wg.Wait()

	// 3. Track failures/successes for auto-disable
	for _, result := range results {
		rm.trackResult(result.RegistryName, result.Error)
	}

	// 4. Merge + deduplicate
	merged := rm.mergeResults(results)

	// 5. Cache the merged results
	if len(merged) > 0 {
		rm.searchCache.Put(query, merged)
	}

	return merged, results
}

// Install downloads and installs a skill from the best matching registry.
func (rm *RegistryManager) Install(ctx context.Context, nameOrSlug string) (*InstallResult, error) {
	// Search for the skill across all registries
	merged, results := rm.SearchAll(ctx, nameOrSlug)

	// Find exact match
	var meta *SkillMeta
	for i, m := range merged {
		if m.Name == nameOrSlug || m.Slug == nameOrSlug {
			meta = &merged[i]
			break
		}
	}

	// If no exact match, try GetSkillMeta on each registry
	if meta == nil {
		rm.mu.RLock()
		regs := rm.enabledRegistries()
		rm.mu.RUnlock()

		for _, reg := range regs {
			m, err := reg.GetSkillMeta(ctx, nameOrSlug)
			if err != nil {
				rm.trackResult(reg.Name(), err)
				continue
			}
			if m != nil && m.Name != "" {
				rm.trackResult(reg.Name(), nil)
				meta = m
				break
			}
		}
	}

	if meta == nil {
		// Build helpful error listing which registries were tried
		var tried []string
		for _, r := range results {
			if r.Error != nil {
				tried = append(tried, fmt.Sprintf("%s (%s)", r.RegistryName, shortenError(r.Error)))
			} else {
				tried = append(tried, r.RegistryName)
			}
		}
		return nil, fmt.Errorf("skill '%s' not found. Searched: %s", nameOrSlug, strings.Join(tried, ", "))
	}

	// Check moderation
	if ShouldBlock(meta.Moderation) {
		return nil, fmt.Errorf("%s", CheckModeration(meta))
	}

	// Find the registry that has this skill and download
	rm.mu.RLock()
	regs := rm.enabledRegistries()
	rm.mu.RUnlock()

	var content []byte
	var downloadErr error
	for _, reg := range regs {
		if reg.Name() == meta.RegistryName {
			content, downloadErr = reg.DownloadSkill(ctx, meta)
			if downloadErr == nil {
				rm.trackResult(reg.Name(), nil)
				break
			}
			rm.trackResult(reg.Name(), downloadErr)
		}
	}
	if content == nil && downloadErr != nil {
		return nil, fmt.Errorf("downloading skill: %w", downloadErr)
	}
	if content == nil {
		return nil, fmt.Errorf("could not download skill '%s'", nameOrSlug)
	}

	// Install to disk — use snapshot-based install for skills.sh, regular for others
	var result *InstallResult
	var installErr error
	if IsSkillsShSource(meta.RegistryName) {
		files, hash, parseErr := ParseSnapshotFiles(content)
		if parseErr != nil {
			// Fallback to regular install if snapshot parsing fails
			rm.logger.Debug("snapshot parse failed, falling back to regular install",
				zap.String("name", nameOrSlug), zap.Error(parseErr))
			result, installErr = rm.installer.Install(meta, content)
		} else {
			result, installErr = rm.installer.InstallFromSnapshot(meta, files, hash)
		}
	} else {
		result, installErr = rm.installer.Install(meta, content)
	}
	if installErr != nil {
		return nil, installErr
	}

	// Invalidate search cache for this skill name
	rm.searchCache.Invalidate(nameOrSlug)

	return result, nil
}

// InstallFrom downloads and installs a skill from a SPECIFIC registry.
// Unlike Install() which picks the first match, this targets the named registry.
func (rm *RegistryManager) InstallFrom(ctx context.Context, nameOrSlug string, registryName string) (*InstallResult, error) {
	rm.mu.RLock()
	regs := rm.enabledRegistries()
	rm.mu.RUnlock()

	// Find the target registry
	var targetReg SkillRegistry
	for _, reg := range regs {
		if reg.Name() == registryName {
			targetReg = reg
			break
		}
	}
	if targetReg == nil {
		return nil, fmt.Errorf("registry '%s' not found or not enabled", registryName)
	}

	// Get metadata from the target registry
	meta, err := targetReg.GetSkillMeta(ctx, nameOrSlug)
	if err != nil {
		rm.trackResult(registryName, err)
		return nil, fmt.Errorf("skill '%s' not found in %s: %w", nameOrSlug, registryName, err)
	}
	rm.trackResult(registryName, nil)

	// Check moderation
	if ShouldBlock(meta.Moderation) {
		return nil, fmt.Errorf("%s", CheckModeration(meta))
	}

	// Download from the target registry
	content, err := targetReg.DownloadSkill(ctx, meta)
	if err != nil {
		rm.trackResult(registryName, err)
		return nil, fmt.Errorf("downloading skill: %w", err)
	}
	rm.trackResult(registryName, nil)

	// Install to disk
	var result *InstallResult
	var installErr error
	if IsSkillsShSource(meta.RegistryName) {
		files, hash, parseErr := ParseSnapshotFiles(content)
		if parseErr != nil {
			rm.logger.Debug("snapshot parse failed, falling back to regular install",
				zap.String("name", nameOrSlug), zap.Error(parseErr))
			result, installErr = rm.installer.Install(meta, content)
		} else {
			result, installErr = rm.installer.InstallFromSnapshot(meta, files, hash)
		}
	} else {
		result, installErr = rm.installer.Install(meta, content)
	}
	if installErr != nil {
		return nil, installErr
	}

	rm.searchCache.Invalidate(nameOrSlug)
	return result, nil
}

// Uninstall removes an installed skill from disk.
func (rm *RegistryManager) Uninstall(name string) error {
	err := rm.installer.Uninstall(name)
	if err != nil {
		return err
	}
	rm.searchCache.Invalidate(name)
	return nil
}

// IsInstalled checks if a skill is installed locally (by exact name).
func (rm *RegistryManager) IsInstalled(name string) bool {
	return rm.installer.IsInstalled(name)
}

// IsInstalledFromSource checks if a skill with the given base name is installed
// from a specific registry source.
func (rm *RegistryManager) IsInstalledFromSource(baseName string, registryName string) bool {
	return rm.installer.IsInstalledFromSource(baseName, registryName)
}

// IsInstalledAny checks if any version of a skill (by base name) is installed,
// regardless of source.
func (rm *RegistryManager) IsInstalledAny(baseName string) bool {
	return len(rm.installer.FindInstalled(baseName)) > 0
}

// GetInstalledInfo returns metadata for a specific installed skill.
func (rm *RegistryManager) GetInstalledInfo(name string) *InstalledSkillInfo {
	return rm.installer.GetInstalledInfo(name)
}

// GetAllInstalledInfo returns all installed skills matching a base name.
func (rm *RegistryManager) GetAllInstalledInfo(name string) []InstalledSkillInfo {
	return rm.installer.GetAllInstalledInfo(name)
}

// ListInstalled returns all locally installed skills.
func (rm *RegistryManager) ListInstalled() ([]InstalledSkillInfo, error) {
	return rm.installer.ListInstalled()
}

// GetRegistries returns info about all configured registries.
func (rm *RegistryManager) GetRegistries() []RegistryInfo {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	now := time.Now()
	var infos []RegistryInfo
	for _, entry := range rm.config.Registries {
		info := RegistryInfo{
			Name:    entry.Name,
			URL:     entry.URL,
			Enabled: entry.IsActive,
		}
		// Check temporary disable status
		if until, ok := rm.disabledUntil[entry.Name]; ok && now.Before(until) {
			info.TempDisabled = true
			info.DisabledUntil = &until
			info.FailureCount = rm.failureCounts[entry.Name]
		}
		infos = append(infos, info)
	}
	return infos
}

// GetInstallDir returns the installation directory.
func (rm *RegistryManager) GetInstallDir() string {
	return rm.installer.GetInstallDir()
}

// GetConfigPath returns the config file path.
func (rm *RegistryManager) GetConfigPath() string {
	return ConfigPath()
}

// ClearCache clears the search cache.
func (rm *RegistryManager) ClearCache() {
	rm.searchCache.Clear()
}

// GetSkillMeta retrieves metadata for a skill (returns first found across registries).
// Skips results with empty names.
func (rm *RegistryManager) GetSkillMeta(ctx context.Context, nameOrSlug string) (*SkillMeta, error) {
	rm.mu.RLock()
	regs := rm.enabledRegistries()
	rm.mu.RUnlock()

	for _, reg := range regs {
		meta, err := reg.GetSkillMeta(ctx, nameOrSlug)
		if err != nil {
			rm.trackResult(reg.Name(), err)
			continue
		}
		if meta != nil && meta.Name != "" {
			rm.trackResult(reg.Name(), nil)
			return meta, nil
		}
	}
	return nil, fmt.Errorf("skill '%s' not found in any registry", nameOrSlug)
}

// GetSkillMetaFrom retrieves metadata for a skill from a SPECIFIC registry.
func (rm *RegistryManager) GetSkillMetaFrom(ctx context.Context, nameOrSlug string, registryName string) (*SkillMeta, error) {
	rm.mu.RLock()
	regs := rm.enabledRegistries()
	rm.mu.RUnlock()

	for _, reg := range regs {
		if reg.Name() != registryName {
			continue
		}
		meta, err := reg.GetSkillMeta(ctx, nameOrSlug)
		if err != nil {
			rm.trackResult(reg.Name(), err)
			return nil, err
		}
		if meta != nil && meta.Name != "" {
			rm.trackResult(reg.Name(), nil)
			return meta, nil
		}
	}
	return nil, fmt.Errorf("skill '%s' not found in registry '%s'", nameOrSlug, registryName)
}

// GetAllSkillMeta retrieves metadata for a skill from ALL enabled registries.
// Unlike GetSkillMeta which returns the first match, this returns results from
// every registry that has the skill. Used by Info to show comprehensive data.
func (rm *RegistryManager) GetAllSkillMeta(ctx context.Context, nameOrSlug string) []*SkillMeta {
	rm.mu.RLock()
	regs := rm.enabledRegistries()
	rm.mu.RUnlock()

	var results []*SkillMeta
	for _, reg := range regs {
		meta, err := reg.GetSkillMeta(ctx, nameOrSlug)
		if err != nil {
			rm.trackResult(reg.Name(), err)
			continue
		}
		if meta != nil && meta.Name != "" {
			rm.trackResult(reg.Name(), nil)
			results = append(results, meta)
		}
	}
	return results
}

// trackResult updates the failure counter for a registry.
// After maxFailures consecutive errors, the registry is auto-disabled for cooldownDuration.
func (rm *RegistryManager) trackResult(registryName string, err error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if err != nil {
		rm.failureCounts[registryName]++
		count := rm.failureCounts[registryName]
		if count >= rm.maxFailures {
			until := time.Now().Add(rm.cooldownDuration)
			rm.disabledUntil[registryName] = until
			rm.logger.Warn("registry temporarily disabled after repeated failures",
				zap.String("registry", registryName),
				zap.Int("failures", count),
				zap.Duration("cooldown", rm.cooldownDuration),
			)
		}
	} else {
		// Success resets the failure counter and re-enables
		delete(rm.failureCounts, registryName)
		delete(rm.disabledUntil, registryName)
	}
}

// enabledRegistries returns only enabled registries that are not in cooldown.
func (rm *RegistryManager) enabledRegistries() []SkillRegistry {
	var enabled []SkillRegistry
	now := time.Now()
	for _, r := range rm.registries {
		if !r.Enabled() {
			continue
		}
		// Skip registries in auto-disable cooldown
		if until, ok := rm.disabledUntil[r.Name()]; ok && now.Before(until) {
			continue
		}
		enabled = append(enabled, r)
	}
	return enabled
}

// mergeResults merges and deduplicates results from multiple registries.
// Skills with the same name from DIFFERENT registries are kept (both shown),
// but duplicates within the SAME registry are removed.
// Skips entries with empty names.
func (rm *RegistryManager) mergeResults(results []SearchResult) []SkillMeta {
	seen := make(map[string]bool)
	var merged []SkillMeta

	for _, result := range results {
		if result.Error != nil {
			continue
		}
		for _, skill := range result.Skills {
			// Normalize: use Slug as fallback for empty Name
			if skill.Name == "" {
				skill.Name = skill.Slug
			}
			if skill.Name == "" {
				continue // skip skills with no identifiable name
			}
			// Dedup key includes registry so same-name skills from different
			// registries both appear in results. This is critical because
			// "frontend-design" from clawhub and "frontend-design" from
			// skills.sh are different skills with different content/metadata.
			key := skill.RegistryName + "/" + skill.Name
			if !seen[key] {
				seen[key] = true
				merged = append(merged, skill)
			}
		}
	}

	// Sort by name, then by registry for deterministic output
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Name != merged[j].Name {
			return merged[i].Name < merged[j].Name
		}
		return merged[i].RegistryName < merged[j].RegistryName
	})

	return merged
}

// shortenError returns a concise version of a registry error message.
func shortenError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "no such host") {
		return "DNS lookup failed"
	}
	if strings.Contains(msg, "connection refused") {
		return "connection refused"
	}
	if strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "timeout") {
		return "timeout"
	}
	if strings.Contains(msg, "certificate") {
		return "TLS error"
	}
	if len(msg) > 60 {
		return msg[:57] + "..."
	}
	return msg
}
