/*
 * ChatCLI - ChatCLI.dev Registry Adapter
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 *
 * Implements SkillRegistry for the official ChatCLI skill registry.
 * Migrated and enhanced from cli/skills/registry.go.
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
	chatcliUserAgent   = "chatcli-registry/1.0"
	chatcliMaxBody     = 2 << 20  // 2MB max response
	chatcliMaxDownload = 10 << 20 // 10MB max skill download
)

// ChatCLIRegistry implements SkillRegistry for registry.chatcli.dev.
type ChatCLIRegistry struct {
	name       string
	baseURL    string
	enabled    bool
	token      string
	httpClient *http.Client
	cacheTTL   time.Duration
	cache      map[string]chatcliCacheEntry
	mu         sync.RWMutex
	logger     *zap.Logger
}

type chatcliCacheEntry struct {
	skills   []SkillMeta
	cachedAt time.Time
}

// chatcliSearchResponse is the API response for search.
type chatcliSearchResponse struct {
	Skills []chatcliSkillEntry `json:"skills"`
}

type chatcliSkillEntry struct {
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

// NewChatCLIRegistry creates a new ChatCLI.dev registry adapter.
func NewChatCLIRegistry(entry RegistryEntry, logger *zap.Logger) *ChatCLIRegistry {
	cacheTTL := entry.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = 15 * time.Minute
	}
	return &ChatCLIRegistry{
		name:    entry.Name,
		baseURL: entry.URL,
		enabled: entry.IsActive,
		token:   entry.Token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		cacheTTL: cacheTTL,
		cache:    make(map[string]chatcliCacheEntry),
		logger:   logger,
	}
}

func (r *ChatCLIRegistry) Name() string  { return r.name }
func (r *ChatCLIRegistry) Enabled() bool { return r.enabled }

// Search queries the ChatCLI registry for skills matching a query.
func (r *ChatCLIRegistry) Search(ctx context.Context, query string) ([]SkillMeta, error) {
	// Check per-registry cache
	r.mu.RLock()
	cached, ok := r.cache[query]
	r.mu.RUnlock()

	if ok && time.Since(cached.cachedAt) < r.cacheTTL {
		return cached.skills, nil
	}

	// Build request
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

	var searchResp chatcliSearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, chatcliMaxBody)).Decode(&searchResp); err != nil {
		// Try decoding as plain array (backward compatibility)
		var entries []chatcliSkillEntry
		if err2 := json.NewDecoder(io.LimitReader(resp.Body, chatcliMaxBody)).Decode(&entries); err2 != nil {
			return nil, fmt.Errorf("decoding %s response: %w", r.name, err)
		}
		searchResp.Skills = entries
	}

	// Convert to SkillMeta
	skills := make([]SkillMeta, 0, len(searchResp.Skills))
	for _, e := range searchResp.Skills {
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

	// Update cache
	r.mu.Lock()
	r.cache[query] = chatcliCacheEntry{skills: skills, cachedAt: time.Now()}
	r.mu.Unlock()

	return skills, nil
}

// GetSkillMeta returns full metadata for a specific skill.
func (r *ChatCLIRegistry) GetSkillMeta(ctx context.Context, nameOrSlug string) (*SkillMeta, error) {
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

	var entry chatcliSkillEntry
	if err := json.NewDecoder(io.LimitReader(resp.Body, chatcliMaxBody)).Decode(&entry); err != nil {
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
func (r *ChatCLIRegistry) DownloadSkill(ctx context.Context, meta *SkillMeta) ([]byte, error) {
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

	content, err := io.ReadAll(io.LimitReader(resp.Body, chatcliMaxDownload))
	if err != nil {
		return nil, fmt.Errorf("reading skill content: %w", err)
	}

	return content, nil
}

func (r *ChatCLIRegistry) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", chatcliUserAgent)
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
}
