/*
 * ChatCLI - Registry Manager
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 *
 * Coordinates fan-out parallel search across multiple skill registries.
 */
package registry

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
)

// RegistryManager coordinates multiple registries for fan-out search.
type RegistryManager struct {
	registries    []SkillRegistry
	installer     *Installer
	searchCache   *TrigramCache
	config        RegistriesConfig
	maxConcurrent int
	logger        *zap.Logger
	mu            sync.RWMutex
}

// NewRegistryManager creates a new registry manager from configuration.
func NewRegistryManager(cfg RegistriesConfig, logger *zap.Logger) (*RegistryManager, error) {
	installer := NewInstaller(cfg.InstallDir, logger)

	cacheTTL := 5 * time.Minute
	rm := &RegistryManager{
		installer:     installer,
		searchCache:   NewTrigramCache(cfg.SearchCacheSize, cacheTTL),
		config:        cfg,
		maxConcurrent: cfg.MaxConcurrent,
		logger:        logger,
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

			if err != nil {
				rm.logger.Warn("registry search failed",
					zap.String("registry", r.Name()),
					zap.Error(err),
				)
			}
		}(i, reg)
	}
	wg.Wait()

	// 3. Merge + deduplicate
	merged := rm.mergeResults(results)

	// 4. Cache the merged results
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
			if err == nil && m != nil {
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
				tried = append(tried, fmt.Sprintf("%s (error: %v)", r.RegistryName, r.Error))
			} else {
				tried = append(tried, r.RegistryName)
			}
		}
		return nil, fmt.Errorf("skill '%s' not found in any registry. Searched: %v", nameOrSlug, tried)
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
				break
			}
		}
	}
	if content == nil && downloadErr != nil {
		return nil, fmt.Errorf("downloading skill: %w", downloadErr)
	}
	if content == nil {
		return nil, fmt.Errorf("could not download skill '%s'", nameOrSlug)
	}

	// Install to disk
	result, err := rm.installer.Install(meta, content)
	if err != nil {
		return nil, err
	}

	// Invalidate search cache for this skill name
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

// IsInstalled checks if a skill is installed locally.
func (rm *RegistryManager) IsInstalled(name string) bool {
	return rm.installer.IsInstalled(name)
}

// ListInstalled returns all locally installed skills.
func (rm *RegistryManager) ListInstalled() ([]InstalledSkillInfo, error) {
	return rm.installer.ListInstalled()
}

// GetRegistries returns info about all configured registries.
func (rm *RegistryManager) GetRegistries() []RegistryInfo {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	var infos []RegistryInfo
	for _, entry := range rm.config.Registries {
		infos = append(infos, RegistryInfo{
			Name:    entry.Name,
			URL:     entry.URL,
			Enabled: entry.IsActive,
		})
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
func (rm *RegistryManager) GetSkillMeta(ctx context.Context, nameOrSlug string) (*SkillMeta, error) {
	rm.mu.RLock()
	regs := rm.enabledRegistries()
	rm.mu.RUnlock()

	for _, reg := range regs {
		meta, err := reg.GetSkillMeta(ctx, nameOrSlug)
		if err == nil && meta != nil {
			return meta, nil
		}
	}
	return nil, fmt.Errorf("skill '%s' not found in any registry", nameOrSlug)
}

// enabledRegistries returns only enabled registries.
func (rm *RegistryManager) enabledRegistries() []SkillRegistry {
	var enabled []SkillRegistry
	for _, r := range rm.registries {
		if r.Enabled() {
			enabled = append(enabled, r)
		}
	}
	return enabled
}

// mergeResults merges and deduplicates results from multiple registries.
// First registry wins for skills with the same name.
func (rm *RegistryManager) mergeResults(results []SearchResult) []SkillMeta {
	seen := make(map[string]bool)
	var merged []SkillMeta

	for _, result := range results {
		if result.Error != nil {
			continue
		}
		for _, skill := range result.Skills {
			key := skill.Name
			if !seen[key] {
				seen[key] = true
				merged = append(merged, skill)
			}
		}
	}

	// Sort by name for deterministic output
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Name < merged[j].Name
	})

	return merged
}
