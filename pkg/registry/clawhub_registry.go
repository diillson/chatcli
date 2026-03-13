/*
 * ChatCLI - ClawHub Registry Adapter
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements SkillRegistry for clawhub.ai — a public skill marketplace.
 * Based on the ClawHub HTTP API v1:
 *   - GET /api/v1/search?q=<query>&limit=<n>
 *   - GET /api/v1/skills/<slug>
 *   - GET /api/v1/download?slug=<s>&version=<v>
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

// ClawHub API response types — matching actual clawhub.ai JSON shapes.

type clawhubSearchResponse struct {
	Results []clawhubSearchEntry `json:"results"`
}

type clawhubSearchEntry struct {
	Score       float64 `json:"score"`
	Slug        string  `json:"slug"`
	DisplayName string  `json:"displayName"`
	Summary     string  `json:"summary"`
	Version     *string `json:"version"` // nullable
	UpdatedAt   int64   `json:"updatedAt"`
}

type clawhubSkillDetailResponse struct {
	Skill         clawhubSkillDetail  `json:"skill"`
	LatestVersion *clawhubVersionInfo `json:"latestVersion"`
	Owner         *clawhubOwnerInfo   `json:"owner"`
	Moderation    *clawhubModeration  `json:"moderation"`
}

type clawhubSkillDetail struct {
	Slug        string            `json:"slug"`
	DisplayName string            `json:"displayName"`
	Summary     string            `json:"summary"`
	Tags        map[string]string `json:"tags"`
	Stats       clawhubStats      `json:"stats"`
	CreatedAt   int64             `json:"createdAt"`
	UpdatedAt   int64             `json:"updatedAt"`
}

type clawhubStats struct {
	Comments        int `json:"comments"`
	Downloads       int `json:"downloads"`
	InstallsAllTime int `json:"installsAllTime"`
	InstallsCurrent int `json:"installsCurrent"`
	Stars           int `json:"stars"`
	Versions        int `json:"versions"`
}

type clawhubVersionInfo struct {
	Version   string `json:"version"`
	CreatedAt int64  `json:"createdAt"`
	Changelog string `json:"changelog"`
}

type clawhubOwnerInfo struct {
	Handle      string `json:"handle"`
	DisplayName string `json:"displayName"`
	Image       string `json:"image"`
}

type clawhubModeration struct {
	Flagged bool   `json:"flagged"`
	Reason  string `json:"reason"`
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
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024))
		detail := ""
		if readErr == nil {
			detail = string(body)
		}
		return nil, fmt.Errorf("%s returned %d: %s", r.name, resp.StatusCode, detail)
	}

	var searchResp clawhubSearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, clawhubMaxBody)).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("decoding %s response: %w", r.name, err)
	}

	skills := make([]SkillMeta, 0, len(searchResp.Results))
	for _, e := range searchResp.Results {
		name := e.DisplayName
		if name == "" || name == "Skill" {
			name = e.Slug // fallback: some skills have generic "Skill" as displayName
		}
		if name == "" {
			continue
		}

		version := ""
		if e.Version != nil {
			version = *e.Version
		}

		skills = append(skills, SkillMeta{
			Name:         name,
			Slug:         e.Slug,
			Description:  e.Summary,
			Version:      version,
			RegistryName: r.name,
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
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024))
		detail := ""
		if readErr == nil {
			detail = string(body)
		}
		return nil, fmt.Errorf("%s returned %d: %s", r.name, resp.StatusCode, detail)
	}

	var detail clawhubSkillDetailResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, clawhubMaxBody)).Decode(&detail); err != nil {
		return nil, fmt.Errorf("decoding skill meta: %w", err)
	}

	skill := detail.Skill
	name := skill.DisplayName
	if name == "" || name == "Skill" {
		name = skill.Slug
	}
	if name == "" {
		return nil, fmt.Errorf("skill '%s' returned empty name from %s", nameOrSlug, r.name)
	}

	version := ""
	if detail.LatestVersion != nil {
		version = detail.LatestVersion.Version
	} else if v, ok := skill.Tags["latest"]; ok {
		version = v
	}

	author := ""
	if detail.Owner != nil {
		author = detail.Owner.DisplayName
		if author == "" {
			author = detail.Owner.Handle
		}
	}

	// Map moderation
	var modFlags ModerationFlags
	if detail.Moderation != nil && detail.Moderation.Flagged {
		modFlags.SuspiciousContent = true
		modFlags.Reason = detail.Moderation.Reason
	}

	downloadURL := fmt.Sprintf("%s/download?slug=%s", r.baseURL, url.QueryEscape(skill.Slug))
	if version != "" {
		downloadURL += "&version=" + url.QueryEscape(version)
	}

	return &SkillMeta{
		Name:         name,
		Slug:         skill.Slug,
		Description:  skill.Summary,
		Version:      version,
		Author:       author,
		Downloads:    skill.Stats.Downloads,
		DownloadURL:  downloadURL,
		RegistryName: r.name,
		Moderation:   modFlags,
	}, nil
}

// DownloadSkill downloads the skill content as a zip.
func (r *ClawHubRegistry) DownloadSkill(ctx context.Context, meta *SkillMeta) ([]byte, error) {
	downloadURL := meta.DownloadURL
	if downloadURL == "" {
		downloadURL = fmt.Sprintf("%s/download?slug=%s", r.baseURL, url.QueryEscape(meta.Slug))
		if meta.Version != "" {
			downloadURL += "&version=" + url.QueryEscape(meta.Version)
		}
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
