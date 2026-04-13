/*
 * ChatCLI - Skill Registry Configuration
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package registry

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// RegistryEntry represents a single registry in the user's config.
type RegistryEntry struct {
	Name     string        `yaml:"name" json:"name"`
	URL      string        `yaml:"url" json:"url"`
	IsActive bool          `yaml:"enabled" json:"enabled"`
	CacheTTL time.Duration `yaml:"cache_ttl" json:"cache_ttl"`
	Token    string        `yaml:"token,omitempty" json:"token,omitempty"`
	Type     string        `yaml:"type,omitempty" json:"type,omitempty"` // "chatcli", "clawhub", "custom"
}

// RegistriesConfig is the top-level config for all registries.
type RegistriesConfig struct {
	Registries      []RegistryEntry `yaml:"registries" json:"registries"`
	InstallDir      string          `yaml:"install_dir,omitempty" json:"install_dir,omitempty"`
	MaxConcurrent   int             `yaml:"max_concurrent,omitempty" json:"max_concurrent,omitempty"`
	SearchCacheSize int             `yaml:"search_cache_size,omitempty" json:"search_cache_size,omitempty"`
}

// DefaultConfig returns the default registries configuration.
func DefaultConfig() RegistriesConfig {
	homeDir, _ := os.UserHomeDir()
	return RegistriesConfig{
		Registries: []RegistryEntry{
			{
				Name:     "chatcli",
				URL:      "https://registry.chatcli.dev/api/v1",
				IsActive: true,
				CacheTTL: 15 * time.Minute,
				Type:     "chatcli",
			},
			{
				Name:     "clawhub",
				URL:      "https://clawhub.ai/api/v1",
				IsActive: true,
				CacheTTL: 5 * time.Minute,
				Type:     "clawhub",
			},
			{
				Name:     "skills.sh",
				URL:      "https://skills.sh",
				IsActive: true,
				CacheTTL: 10 * time.Minute,
				Type:     "skillssh",
			},
		},
		InstallDir:      filepath.Join(homeDir, ".chatcli", "skills"),
		MaxConcurrent:   3,
		SearchCacheSize: 50,
	}
}

// ConfigPath returns the path to the registries config file.
func ConfigPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".chatcli", "registries.yaml")
}

// LoadConfig reads the registries config from disk.
// If the file doesn't exist, it creates it with defaults.
func LoadConfig() (RegistriesConfig, error) {
	configPath := filepath.Clean(ConfigPath())

	data, err := os.ReadFile(configPath) // #nosec G304 -- path from ConfigPath (user home dir)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			if saveErr := SaveConfig(cfg); saveErr != nil {
				// Non-fatal: use defaults even if save fails
				return cfg, nil
			}
			return cfg, nil
		}
		return RegistriesConfig{}, err
	}

	var cfg RegistriesConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return RegistriesConfig{}, err
	}

	// Apply defaults for missing fields
	if cfg.InstallDir == "" {
		homeDir, _ := os.UserHomeDir()
		cfg.InstallDir = filepath.Join(homeDir, ".chatcli", "skills")
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 3
	}
	if cfg.SearchCacheSize <= 0 {
		cfg.SearchCacheSize = 50
	}

	// Merge default registries: add any built-in registries that are missing
	// from the user's config. This ensures new registries (like skills.sh)
	// appear automatically after an upgrade without requiring manual edits.
	cfg = mergeDefaultRegistries(cfg)

	// Apply environment variable overrides
	cfg = applyEnvOverrides(cfg)

	return cfg, nil
}

// SaveConfig writes the registries config to disk.
func SaveConfig(cfg RegistriesConfig) error {
	configPath := ConfigPath()

	if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0o600)
}

// applyEnvOverrides applies environment variable overrides to the config.
func applyEnvOverrides(cfg RegistriesConfig) RegistriesConfig {
	// CHATCLI_REGISTRY_URLS: comma-separated additional registry URLs
	if urls := os.Getenv("CHATCLI_REGISTRY_URLS"); urls != "" {
		for _, url := range strings.Split(urls, ",") {
			url = strings.TrimSpace(url)
			if url == "" {
				continue
			}
			// Check if already exists
			exists := false
			for _, r := range cfg.Registries {
				if r.URL == url {
					exists = true
					break
				}
			}
			if !exists {
				// Derive name from URL host
				name := deriveNameFromURL(url)
				cfg.Registries = append(cfg.Registries, RegistryEntry{
					Name:     name,
					URL:      url,
					IsActive: true,
					CacheTTL: 10 * time.Minute,
					Type:     "custom",
				})
			}
		}
	}

	// CHATCLI_REGISTRY_DISABLE: comma-separated registry names to disable
	if disable := os.Getenv("CHATCLI_REGISTRY_DISABLE"); disable != "" {
		disabled := make(map[string]bool)
		for _, name := range strings.Split(disable, ",") {
			disabled[strings.TrimSpace(name)] = true
		}
		for i := range cfg.Registries {
			if disabled[cfg.Registries[i].Name] {
				cfg.Registries[i].IsActive = false
			}
		}
	}

	// CHATCLI_SKILL_INSTALL_DIR: override install directory
	if dir := os.Getenv("CHATCLI_SKILL_INSTALL_DIR"); dir != "" {
		cfg.InstallDir = dir
	}

	return cfg
}

// mergeDefaultRegistries adds any built-in default registries that are not
// already present in the user's config. This allows new registries introduced
// in upgrades (e.g. skills.sh) to appear automatically without requiring
// users to manually edit registries.yaml. Existing entries are never modified.
func mergeDefaultRegistries(cfg RegistriesConfig) RegistriesConfig {
	defaults := DefaultConfig()

	existing := make(map[string]bool, len(cfg.Registries))
	for _, r := range cfg.Registries {
		existing[r.Name] = true
	}

	changed := false
	for _, def := range defaults.Registries {
		if !existing[def.Name] {
			cfg.Registries = append(cfg.Registries, def)
			changed = true
		}
	}

	// Persist the merged config so it only happens once
	if changed {
		_ = SaveConfig(cfg)
	}

	return cfg
}

// deriveNameFromURL extracts a short name from a URL for display.
func deriveNameFromURL(url string) string {
	// Remove protocol
	name := strings.TrimPrefix(url, "https://")
	name = strings.TrimPrefix(name, "http://")
	// Take first part of host
	if idx := strings.Index(name, "/"); idx > 0 {
		name = name[:idx]
	}
	// Remove common suffixes
	name = strings.TrimSuffix(name, ".dev")
	name = strings.TrimSuffix(name, ".ai")
	name = strings.TrimSuffix(name, ".com")
	name = strings.TrimSuffix(name, ".io")
	// Replace dots with dashes
	name = strings.ReplaceAll(name, ".", "-")
	return name
}
