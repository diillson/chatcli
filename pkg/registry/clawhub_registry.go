/*
 * ChatCLI - ClawHub Registry Adapter
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 *
 * Implements SkillRegistry for clawhub.ai — a public skill marketplace.
 * Inspired by PicoClaw's ClawHub integration.
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
	clawhubUserAgent   = "chatcli-clawhub/1.0"
	clawhubMaxBody     = 2 << 20  // 2MB max response
	clawhubMaxDownload = 50 << 20 // 50MB max download
	clawhubSearchLimit = 20
)

// ClawHubRegistry implements SkillRegistry for clawhub.ai.
type ClawHubRegistry struct {
	name       string
	baseURL    string
	enabled    bool
	token      string
	httpClient *http.Client
	cacheTTL   time.Duration
	cache      map[string]clawhubCacheEntry
	mu         sync.RWMutex
	logger     *zap.Logger
}

type clawhubCacheEntry struct {
	skills   []SkillMeta
	cachedAt time.Time
}

// ClawHub API response types
type clawhubSearchResponse struct {
	Results []clawhubSkillEntry `json:"results"`
	Total   int                 `json:"total"`
}

type clawhubSkillEntry struct {
	Name              string   `json:"name"`
	Slug              string   `json:"slug"`
	Description       string   `json:"description"`
	Version           string   `json:"version"`
	Author            string   `json:"author"`
	Tags              []string `json:"tags"`
	Downloads         int      `json:"downloads"`
	DownloadURL       string   `json:"download_url"`
	MalwareDetected   bool     `json:"malware_detected"`
	SuspiciousContent bool     `json:"suspicious_content"`
	ModerationNote    string   `json:"moderation_note"`
}

// NewClawHubRegistry creates a new ClawHub registry adapter.
func NewClawHubRegistry(entry RegistryEntry, logger *zap.Logger) *ClawHubRegistry {
	cacheTTL := entry.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = 5 * time.Minute
	}
	return &ClawHubRegistry{
		name:    entry.Name,
		baseURL: entry.URL,
		enabled: entry.IsActive,
		token:   entry.Token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		cacheTTL: cacheTTL,
		cache:    make(map[string]clawhubCacheEntry),
		logger:   logger,
	}
}

func (r *ClawHubRegistry) Name() string  { return r.name }
func (r *ClawHubRegistry) Enabled() bool { return r.enabled }

// Search queries ClawHub for skills matching a query.
func (r *ClawHubRegistry) Search(ctx context.Context, query string) ([]SkillMeta, error) {
	// Check cache
	r.mu.RLock()
	cached, ok := r.cache[query]
	r.mu.RUnlock()

	if ok && time.Since(cached.cachedAt) < r.cacheTTL {
		return cached.skills, nil
	}

	endpoint := fmt.Sprintf("%s/search?q=%s&limit=%d", r.baseURL, url.QueryEscape(query), clawhubSearchLimit)
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

	var searchResp clawhubSearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, clawhubMaxBody)).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("decoding %s response: %w", r.name, err)
	}

	skills := make([]SkillMeta, 0, len(searchResp.Results))
	for _, e := range searchResp.Results {
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
			Moderation: ModerationFlags{
				MalwareDetected:   e.MalwareDetected,
				SuspiciousContent: e.SuspiciousContent,
				Reason:            e.ModerationNote,
			},
		})
	}

	// Update cache
	r.mu.Lock()
	r.cache[query] = clawhubCacheEntry{skills: skills, cachedAt: time.Now()}
	r.mu.Unlock()

	return skills, nil
}

// GetSkillMeta returns full metadata for a specific skill.
func (r *ClawHubRegistry) GetSkillMeta(ctx context.Context, nameOrSlug string) (*SkillMeta, error) {
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

	var entry clawhubSkillEntry
	if err := json.NewDecoder(io.LimitReader(resp.Body, clawhubMaxBody)).Decode(&entry); err != nil {
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
		Moderation: ModerationFlags{
			MalwareDetected:   entry.MalwareDetected,
			SuspiciousContent: entry.SuspiciousContent,
			Reason:            entry.ModerationNote,
		},
	}, nil
}

// DownloadSkill downloads the skill content.
func (r *ClawHubRegistry) DownloadSkill(ctx context.Context, meta *SkillMeta) ([]byte, error) {
	downloadURL := meta.DownloadURL
	if downloadURL == "" {
		downloadURL = fmt.Sprintf("%s/download?slug=%s&version=%s",
			r.baseURL, url.QueryEscape(meta.Slug), url.QueryEscape(meta.Version))
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

	content, err := io.ReadAll(io.LimitReader(resp.Body, clawhubMaxDownload))
	if err != nil {
		return nil, fmt.Errorf("reading skill content: %w", err)
	}

	return content, nil
}

func (r *ClawHubRegistry) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", clawhubUserAgent)
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
}
