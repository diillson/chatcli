package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
)

// RegistryConfig configures the remote skill registry.
type RegistryConfig struct {
	BaseURL  string        `json:"base_url" yaml:"base_url"`
	CacheTTL time.Duration `json:"cache_ttl" yaml:"cache_ttl"`
	Enabled  bool          `json:"enabled" yaml:"enabled"`
}

// DefaultRegistryConfig returns defaults (disabled by default).
func DefaultRegistryConfig() RegistryConfig {
	return RegistryConfig{
		BaseURL:  "https://registry.chatcli.dev/api/v1",
		CacheTTL: 15 * time.Minute,
		Enabled:  false,
	}
}

// SkillEntry is a skill listing from the registry.
type SkillEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Author      string   `json:"author"`
	Tags        []string `json:"tags"`
	Downloads   int      `json:"downloads"`
	URL         string   `json:"url"`
}

// RegistryClient communicates with a remote skill registry.
type RegistryClient struct {
	config     RegistryConfig
	httpClient *http.Client
	cache      *searchCache
	logger     *zap.Logger
}

type searchCache struct {
	results map[string]cachedSearch
	mu      sync.RWMutex
}

type cachedSearch struct {
	skills   []SkillEntry
	cachedAt time.Time
}

// NewRegistryClient creates a new registry client.
func NewRegistryClient(config RegistryConfig, logger *zap.Logger) *RegistryClient {
	return &RegistryClient{
		config: config,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		cache: &searchCache{
			results: make(map[string]cachedSearch),
		},
		logger: logger,
	}
}

// Search queries the registry for skills matching a query.
func (rc *RegistryClient) Search(ctx context.Context, query string) ([]SkillEntry, error) {
	if !rc.config.Enabled {
		return nil, fmt.Errorf("skill registry is disabled")
	}

	// Check cache
	rc.cache.mu.RLock()
	cached, ok := rc.cache.results[query]
	rc.cache.mu.RUnlock()

	if ok && time.Since(cached.cachedAt) < rc.config.CacheTTL {
		return cached.skills, nil
	}

	// Fetch from registry
	url := fmt.Sprintf("%s/skills/search?q=%s", rc.config.BaseURL, query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "chatcli-skill-client/1.0")

	resp, err := rc.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching skills: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("registry returned %d: %s", resp.StatusCode, string(body))
	}

	var entries []SkillEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	// Update cache
	rc.cache.mu.Lock()
	rc.cache.results[query] = cachedSearch{skills: entries, cachedAt: time.Now()}
	rc.cache.mu.Unlock()

	return entries, nil
}

// Install downloads and installs a skill to the target directory.
func (rc *RegistryClient) Install(ctx context.Context, name string, targetDir string) error {
	if !rc.config.Enabled {
		return fmt.Errorf("skill registry is disabled")
	}

	// Search for the exact skill
	entries, err := rc.Search(ctx, name)
	if err != nil {
		return err
	}

	var entry *SkillEntry
	for i, e := range entries {
		if e.Name == name {
			entry = &entries[i]
			break
		}
	}
	if entry == nil {
		return fmt.Errorf("skill %q not found in registry", name)
	}

	if entry.URL == "" {
		return fmt.Errorf("skill %q has no download URL", name)
	}

	// Download skill content
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, entry.URL, nil)
	if err != nil {
		return fmt.Errorf("creating download request: %w", err)
	}

	resp, err := rc.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading skill: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	// Create skill directory and write SKILL.md
	skillDir := filepath.Join(targetDir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("creating skill directory: %w", err)
	}

	content, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10MB max
	if err != nil {
		return fmt.Errorf("reading skill content: %w", err)
	}

	skillFile := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillFile, content, 0o644); err != nil {
		return fmt.Errorf("writing skill file: %w", err)
	}

	rc.logger.Info("skill installed", zap.String("name", name), zap.String("path", skillFile))
	return nil
}

// Uninstall removes an installed skill.
func (rc *RegistryClient) Uninstall(name string, targetDir string) error {
	skillDir := filepath.Join(targetDir, name)
	if _, err := os.Stat(skillDir); os.IsNotExist(err) {
		return fmt.Errorf("skill %q not installed at %s", name, targetDir)
	}
	return os.RemoveAll(skillDir)
}
