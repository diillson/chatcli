package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// CurrentConfigVersion is the latest config schema version.
const CurrentConfigVersion = 1

// MigrationFunc transforms config from version N to N+1.
type MigrationFunc func(values map[string]interface{}) (map[string]interface{}, error)

// ConfigVersion tracks the config schema version on disk.
type ConfigVersion struct {
	Version      int       `json:"version"`
	MigratedAt   time.Time `json:"migrated_at"`
	MigratedFrom int       `json:"migrated_from,omitempty"`
	BackupPath   string    `json:"backup_path,omitempty"`
}

// MigrationRegistry holds all registered migrations.
type MigrationRegistry struct {
	migrations map[int]MigrationFunc
	mu         sync.RWMutex
	logger     *zap.Logger
	configDir  string
}

// NewMigrationRegistry creates a registry with built-in migrations.
func NewMigrationRegistry(configDir string, logger *zap.Logger) *MigrationRegistry {
	r := &MigrationRegistry{
		migrations: make(map[int]MigrationFunc),
		logger:     logger,
		configDir:  configDir,
	}
	r.registerBuiltinMigrations()
	return r
}

// Register adds a migration from fromVersion to fromVersion+1.
func (r *MigrationRegistry) Register(fromVersion int, fn MigrationFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.migrations[fromVersion] = fn
}

// GetCurrentVersion reads the version from disk.
func (r *MigrationRegistry) GetCurrentVersion() (ConfigVersion, error) {
	path := filepath.Join(r.configDir, "config_version.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ConfigVersion{Version: 0}, nil
		}
		return ConfigVersion{}, fmt.Errorf("reading version file: %w", err)
	}
	var cv ConfigVersion
	if err := json.Unmarshal(data, &cv); err != nil {
		return ConfigVersion{}, fmt.Errorf("parsing version file: %w", err)
	}
	return cv, nil
}

// SetVersion writes the version to disk.
func (r *MigrationRegistry) SetVersion(version int) error {
	if err := os.MkdirAll(r.configDir, 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	cv := ConfigVersion{
		Version:    version,
		MigratedAt: time.Now().UTC(),
	}
	data, err := json.MarshalIndent(cv, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling version: %w", err)
	}
	path := filepath.Join(r.configDir, "config_version.json")
	return os.WriteFile(path, data, 0o644)
}

// NeedsMigration checks if migration is required.
func (r *MigrationRegistry) NeedsMigration() (bool, int, int, error) {
	cv, err := r.GetCurrentVersion()
	if err != nil {
		return false, 0, 0, err
	}
	return cv.Version < CurrentConfigVersion, cv.Version, CurrentConfigVersion, nil
}

// Migrate runs all pending migrations sequentially.
func (r *MigrationRegistry) Migrate(values map[string]interface{}) (map[string]interface{}, error) {
	cv, err := r.GetCurrentVersion()
	if err != nil {
		return values, err
	}

	if cv.Version >= CurrentConfigVersion {
		return values, nil
	}

	r.mu.RLock()
	versions := make([]int, 0, len(r.migrations))
	for v := range r.migrations {
		if v >= cv.Version && v < CurrentConfigVersion {
			versions = append(versions, v)
		}
	}
	r.mu.RUnlock()
	sort.Ints(versions)

	// Backup before migration
	backupPath, err := r.Backup(values)
	if err != nil {
		r.logger.Warn("failed to backup config before migration", zap.Error(err))
	}

	startVersion := cv.Version
	current := copyMap(values)

	for _, v := range versions {
		r.mu.RLock()
		fn := r.migrations[v]
		r.mu.RUnlock()

		r.logger.Info("running config migration",
			zap.Int("from", v),
			zap.Int("to", v+1),
		)

		migrated, err := fn(current)
		if err != nil {
			r.logger.Error("migration failed, rolling back",
				zap.Int("version", v),
				zap.Error(err),
			)
			if backupPath != "" {
				if rbErr := r.Rollback(backupPath); rbErr != nil {
					r.logger.Error("rollback also failed", zap.Error(rbErr))
				}
			}
			return values, fmt.Errorf("migration v%d→v%d failed: %w", v, v+1, err)
		}
		current = migrated
	}

	// Update version on disk
	cvNew := ConfigVersion{
		Version:      CurrentConfigVersion,
		MigratedAt:   time.Now().UTC(),
		MigratedFrom: startVersion,
		BackupPath:   backupPath,
	}
	data, _ := json.MarshalIndent(cvNew, "", "  ")
	versionPath := filepath.Join(r.configDir, "config_version.json")
	if err := os.WriteFile(versionPath, data, 0o644); err != nil {
		r.logger.Warn("failed to write version file after migration", zap.Error(err))
	}

	r.logger.Info("config migration completed",
		zap.Int("from", startVersion),
		zap.Int("to", CurrentConfigVersion),
	)

	return current, nil
}

// Backup saves the current config to a timestamped backup file.
func (r *MigrationRegistry) Backup(values map[string]interface{}) (string, error) {
	backupDir := filepath.Join(r.configDir, "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", fmt.Errorf("creating backup dir: %w", err)
	}

	cv, _ := r.GetCurrentVersion()
	ts := time.Now().UTC().Format("20060102T150405")
	filename := fmt.Sprintf("config_v%d_%s.json", cv.Version, ts)
	backupPath := filepath.Join(backupDir, filename)

	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling backup: %w", err)
	}
	if err := os.WriteFile(backupPath, data, 0o644); err != nil {
		return "", fmt.Errorf("writing backup: %w", err)
	}

	r.logger.Info("config backed up", zap.String("path", backupPath))
	return backupPath, nil
}

// Rollback restores config from a backup file.
func (r *MigrationRegistry) Rollback(backupPath string) error {
	data, err := os.ReadFile(backupPath)
	if err != nil {
		return fmt.Errorf("reading backup: %w", err)
	}
	var values map[string]interface{}
	if err := json.Unmarshal(data, &values); err != nil {
		return fmt.Errorf("parsing backup: %w", err)
	}
	r.logger.Info("config rolled back from backup", zap.String("path", backupPath))
	return nil
}

func (r *MigrationRegistry) registerBuiltinMigrations() {
	// v0 → v1: Initial migration
	r.Register(0, func(values map[string]interface{}) (map[string]interface{}, error) {
		result := copyMap(values)

		// Normalize provider names to uppercase
		if provider, ok := result["LLM_PROVIDER"]; ok {
			if s, ok := provider.(string); ok {
				result["LLM_PROVIDER"] = strings.ToUpper(s)
			}
		}

		// Migrate deprecated env var names
		renames := map[string]string{
			"CHATCLI_API_KEY": "OPENAI_API_KEY",
			"CLAUDE_API_KEY":  "CLAUDEAI_API_KEY",
			"GOOGLE_API_KEY":  "GOOGLEAI_API_KEY",
			"GEMINI_API_KEY":  "GOOGLEAI_API_KEY",
		}
		for old, newKey := range renames {
			if val, ok := result[old]; ok {
				if _, exists := result[newKey]; !exists {
					result[newKey] = val
				}
				delete(result, old)
			}
		}

		// Set defaults for new features
		defaults := map[string]interface{}{
			"CHATCLI_SKILLS_ENABLED":    "true",
			"CHATCLI_MEMORY_ENABLED":    "true",
			"CHATCLI_BOOTSTRAP_ENABLED": "true",
		}
		for k, v := range defaults {
			if _, exists := result[k]; !exists {
				result[k] = v
			}
		}

		return result, nil
	})
}

func copyMap(m map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}
