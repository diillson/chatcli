/*
 * ChatCLI - Custom/Generic Registry Adapter
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements SkillRegistry for user-configured registries.
 * Assumes the same API contract as ChatCLI.dev (standard REST endpoints).
 */
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	customUserAgent   = "chatcli-custom-registry/1.0"
	customMaxBody     = 2 << 20  // 2MB
	customMaxDownload = 10 << 20 // 10MB
)

// CustomRegistry implements SkillRegistry for user-configured registries.
// It follows the ChatCLI.dev API contract:
//   - GET /skills/search?q=<query>  -> { "skills": [...] } or [...]
//   - GET /skills/<slug>            -> skill meta object
//   - GET /skills/<slug>/download   -> skill content
type CustomRegistry struct {
	name       string
	baseURL    string
	enabled    bool
	token      string
	httpClient *http.Client
	cacheTTL   time.Duration
	cache      map[string]customCacheEntry
	mu         sync.RWMutex
	logger     *zap.Logger
}

type customCacheEntry struct {
	skills   []SkillMeta
	cachedAt time.Time
}

// customSkillEntry is the expected JSON shape from custom registries.
type customSkillEntry struct {
	Name        string          `json:"name"`
	Slug        string          `json:"slug"`
	Description string          `json:"description"`
	Version     string          `json:"version"`
	Author      string          `json:"author"`
	Tags        []string        `json:"tags"`
	Downloads   int             `json:"downloads"`
	DownloadURL string          `json:"download_url"`
	Moderation  ModerationFlags `json:"moderation"`
}

// NewCustomRegistry creates a new generic registry adapter.
func NewCustomRegistry(entry RegistryEntry, logger *zap.Logger) *CustomRegistry {
	cacheTTL := entry.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = 10 * time.Minute
	}
	return &CustomRegistry{
		name:    entry.Name,
		baseURL: entry.URL,
		enabled: entry.IsActive,
		token:   entry.Token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		cacheTTL: cacheTTL,
		cache:    make(map[string]customCacheEntry),
		logger:   logger,
	}
}

func (r *CustomRegistry) Name() string  { return r.name }
func (r *CustomRegistry) Enabled() bool { return r.enabled }

// Search queries the custom registry for skills matching a query.
func (r *CustomRegistry) Search(ctx context.Context, query string) ([]SkillMeta, error) {
	r.mu.RLock()
	cached, ok := r.cache[query]
	r.mu.RUnlock()

	if ok && time.Since(cached.cachedAt) < r.cacheTTL {
		return cached.skills, nil
	}

	endpoint := fmt.Sprintf("%s/skills/search?q=%s", r.baseURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searching %s: %w", r.name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("%s returned %d: %s", r.name, resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, customMaxBody))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	// Try wrapped format first: { "skills": [...] }
	var wrapped struct {
		Skills []customSkillEntry `json:"skills"`
	}
	var entries []customSkillEntry
	if err := json.Unmarshal(body, &wrapped); err == nil && len(wrapped.Skills) > 0 {
		entries = wrapped.Skills
	} else {
		// Try plain array: [...]
		if err := json.Unmarshal(body, &entries); err != nil {
			return nil, fmt.Errorf("decoding %s response: %w", r.name, err)
		}
	}

	skills := make([]SkillMeta, 0, len(entries))
	for _, e := range entries {
		slug := e.Slug
		if slug == "" {
			slug = e.Name
		}
		skills = append(skills, SkillMeta{
			Name:         e.Name,
			Slug:         slug,
			Description:  e.Description,
			Version:      e.Version,
			Author:       e.Author,
			Tags:         e.Tags,
			Downloads:    e.Downloads,
			DownloadURL:  e.DownloadURL,
			RegistryName: r.name,
			Moderation:   e.Moderation,
		})
	}

	r.mu.Lock()
	r.cache[query] = customCacheEntry{skills: skills, cachedAt: time.Now()}
	r.mu.Unlock()

	return skills, nil
}

// GetSkillMeta returns full metadata for a specific skill.
func (r *CustomRegistry) GetSkillMeta(ctx context.Context, nameOrSlug string) (*SkillMeta, error) {
	endpoint := fmt.Sprintf("%s/skills/%s", r.baseURL, url.PathEscape(nameOrSlug))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching skill meta from %s: %w", r.name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("skill '%s' not found in %s", nameOrSlug, r.name)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("%s returned %d: %s", r.name, resp.StatusCode, string(body))
	}

	var entry customSkillEntry
	if err := json.NewDecoder(io.LimitReader(resp.Body, customMaxBody)).Decode(&entry); err != nil {
		return nil, fmt.Errorf("decoding skill meta: %w", err)
	}

	slug := entry.Slug
	if slug == "" {
		slug = entry.Name
	}

	return &SkillMeta{
		Name:         entry.Name,
		Slug:         slug,
		Description:  entry.Description,
		Version:      entry.Version,
		Author:       entry.Author,
		Tags:         entry.Tags,
		Downloads:    entry.Downloads,
		DownloadURL:  entry.DownloadURL,
		RegistryName: r.name,
		Moderation:   entry.Moderation,
	}, nil
}

// DownloadSkill downloads the skill content.
func (r *CustomRegistry) DownloadSkill(ctx context.Context, meta *SkillMeta) ([]byte, error) {
	downloadURL := meta.DownloadURL
	if downloadURL == "" {
		downloadURL = fmt.Sprintf("%s/skills/%s/download", r.baseURL, url.PathEscape(meta.Slug))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating download request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading from %s: %w", r.name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download from %s returned %d", r.name, resp.StatusCode)
	}

	content, err := io.ReadAll(io.LimitReader(resp.Body, customMaxDownload))
	if err != nil {
		return nil, fmt.Errorf("reading skill content: %w", err)
	}

	return content, nil
}

func (r *CustomRegistry) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", customUserAgent)
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
}
