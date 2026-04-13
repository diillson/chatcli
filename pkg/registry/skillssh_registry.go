/*
 * ChatCLI - Skills.sh Registry Adapter
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements SkillRegistry for skills.sh — the open agent skills directory.
 * Based on the skills.sh HTTP API:
 *   - GET /api/search?q=<query>&limit=<n>          -> search skills
 *   - GET /api/download/<owner>/<repo>/<slug>       -> download skill snapshot
 *   - GitHub API for enriched metadata (stars, etc.)
 *
 * Skills on skills.sh are hosted on GitHub. The search API returns metadata
 * with install counts, and the download API returns pre-built snapshots
 * containing all skill files (SKILL.md, scripts/, references/, assets/).
 */
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	skillsshUserAgent    = "chatcli-skillssh/1.0"
	skillsshMaxBody      = 2 << 20  // 2MB max response
	skillsshMaxDownload  = 50 << 20 // 50MB max download
	skillsshSearchLimit  = 20
	skillsshSearchPath   = "/api/search"
	skillsshDownloadPath = "/api/download"
	skillsshAuditURL     = "https://add-skill.vercel.sh/audit"
	skillsshAuditTimeout = 3 * time.Second
)

// SkillsShRegistry implements SkillRegistry for skills.sh.
type SkillsShRegistry struct {
	name       string
	baseURL    string
	enabled    bool
	httpClient *http.Client
	cacheTTL   time.Duration
	cache      map[string]skillsshCacheEntry
	mu         sync.RWMutex
	logger     *zap.Logger
}

type skillsshCacheEntry struct {
	skills   []SkillMeta
	cachedAt time.Time
}

// --- skills.sh API response types ---

// skillsshSearchResponse is the top-level search response from skills.sh.
type skillsshSearchResponse struct {
	Query      string             `json:"query"`
	SearchType string             `json:"searchType"`
	Skills     []skillsshSkillHit `json:"skills"`
	Count      int                `json:"count"`
	DurationMs int                `json:"duration_ms"`
}

// skillsshSkillHit is a single skill in search results.
type skillsshSkillHit struct {
	ID       string `json:"id"`       // e.g. "anthropics/skills/frontend-design"
	SkillID  string `json:"skillId"`  // e.g. "frontend-design"
	Name     string `json:"name"`     // e.g. "frontend-design"
	Installs int    `json:"installs"` // total install count
	Source   string `json:"source"`   // e.g. "anthropics/skills" (owner/repo)
}

// skillsshDownloadResponse is the download API response with all skill files.
type skillsshDownloadResponse struct {
	Files []skillsshSnapshotFile `json:"files"`
	Hash  string                 `json:"hash"`
}

// skillsshSnapshotFile is a single file in the snapshot.
type skillsshSnapshotFile struct {
	Path     string `json:"path"`
	Contents string `json:"contents"`
}

// NewSkillsShRegistry creates a new skills.sh registry adapter.
func NewSkillsShRegistry(entry RegistryEntry, logger *zap.Logger) *SkillsShRegistry {
	cacheTTL := entry.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = 10 * time.Minute
	}
	baseURL := strings.TrimRight(entry.URL, "/")
	return &SkillsShRegistry{
		name:    entry.Name,
		baseURL: baseURL,
		enabled: entry.IsActive,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		cacheTTL: cacheTTL,
		cache:    make(map[string]skillsshCacheEntry),
		logger:   logger,
	}
}

func (r *SkillsShRegistry) Name() string  { return r.name }
func (r *SkillsShRegistry) Enabled() bool { return r.enabled }

// Search queries skills.sh for skills matching a query.
// API: GET /api/search?q=<query>&limit=<n>
func (r *SkillsShRegistry) Search(ctx context.Context, query string) ([]SkillMeta, error) {
	// Check cache
	r.mu.RLock()
	cached, ok := r.cache[query]
	r.mu.RUnlock()

	if ok && time.Since(cached.cachedAt) < r.cacheTTL {
		return cached.skills, nil
	}

	endpoint := fmt.Sprintf("%s%s?q=%s&limit=%d",
		r.baseURL, skillsshSearchPath,
		url.QueryEscape(query), skillsshSearchLimit)

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

	var searchResp skillsshSearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, skillsshMaxBody)).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("decoding %s response: %w", r.name, err)
	}

	skills := make([]SkillMeta, 0, len(searchResp.Skills))
	for _, hit := range searchResp.Skills {
		name := hit.Name
		if name == "" {
			name = hit.SkillID
		}
		if name == "" {
			continue
		}

		// Extract author from source (owner/repo -> owner)
		author := ""
		if hit.Source != "" {
			if parts := strings.SplitN(hit.Source, "/", 2); len(parts) > 0 {
				author = parts[0]
			}
		}

		skills = append(skills, SkillMeta{
			Name:         name,
			Slug:         hit.ID, // full ID: "owner/repo/skill-name"
			Description:  "",     // search API doesn't return descriptions
			Author:       author,
			Downloads:    hit.Installs,
			DownloadURL:  r.buildDownloadURL(hit.Source, hit.SkillID),
			RegistryName: r.name,
		})
	}

	// Update cache
	r.mu.Lock()
	r.cache[query] = skillsshCacheEntry{skills: skills, cachedAt: time.Now()}
	r.mu.Unlock()

	r.logger.Debug("skills.sh search completed",
		zap.String("query", query),
		zap.Int("results", len(skills)),
		zap.Int("duration_ms", searchResp.DurationMs),
	)

	return skills, nil
}

// GetSkillMeta returns full metadata for a specific skill.
// Accepts formats: "skill-name", "owner/repo/skill-name", or "owner/repo:skill-name".
// Performs a search query to find the skill and enriches with download URL.
func (r *SkillsShRegistry) GetSkillMeta(ctx context.Context, nameOrSlug string) (*SkillMeta, error) {
	// Parse the input to extract skill name and optional owner/repo
	skillName, ownerRepo := r.parseSkillRef(nameOrSlug)

	// Search for the skill
	results, err := r.Search(ctx, skillName)
	if err != nil {
		return nil, err
	}

	// Find exact match
	var best *SkillMeta
	for i, m := range results {
		// Exact match on full ID (owner/repo/skill-name)
		if m.Slug == nameOrSlug {
			best = &results[i]
			break
		}
		// Match on skill name, optionally filtering by owner/repo
		if m.Name == skillName || m.Slug == nameOrSlug {
			if ownerRepo != "" {
				// Check if source matches
				source := r.extractSource(m.Slug)
				if source == ownerRepo {
					best = &results[i]
					break
				}
			} else if best == nil {
				// Take first name match (highest installs, API returns sorted)
				best = &results[i]
			}
		}
	}

	if best == nil {
		return nil, fmt.Errorf("skill '%s' not found in %s", nameOrSlug, r.name)
	}

	// Enrich metadata by fetching SKILL.md frontmatter from download snapshot
	r.enrichMeta(ctx, best)

	return best, nil
}

// DownloadSkill downloads a skill snapshot from skills.sh.
// The response contains all files (SKILL.md, scripts/, references/, assets/)
// encoded as JSON with a content hash for integrity.
//
// Returns the raw JSON snapshot bytes. The installer should use
// InstallFromSnapshot to write these files atomically to disk.
func (r *SkillsShRegistry) DownloadSkill(ctx context.Context, meta *SkillMeta) ([]byte, error) {
	downloadURL := meta.DownloadURL
	if downloadURL == "" {
		// Try to build from slug
		skillName, _ := r.parseSkillRef(meta.Slug)
		source := r.extractSource(meta.Slug)
		if source == "" || skillName == "" {
			return nil, fmt.Errorf("cannot determine download URL for '%s'", meta.Slug)
		}
		downloadURL = r.buildDownloadURL(source, skillName)
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
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024))
		detail := ""
		if readErr == nil {
			detail = string(body)
		}
		return nil, fmt.Errorf("download from %s returned %d: %s", r.name, resp.StatusCode, detail)
	}

	content, err := io.ReadAll(io.LimitReader(resp.Body, skillsshMaxDownload))
	if err != nil {
		return nil, fmt.Errorf("reading skill snapshot: %w", err)
	}

	// Validate that it's a proper snapshot response
	var snapshot skillsshDownloadResponse
	if err := json.Unmarshal(content, &snapshot); err != nil {
		return nil, fmt.Errorf("invalid snapshot format from %s: %w", r.name, err)
	}

	if len(snapshot.Files) == 0 {
		return nil, fmt.Errorf("empty snapshot from %s for '%s'", r.name, meta.Name)
	}

	// Verify at least one SKILL.md exists in the snapshot
	hasSkillMD := false
	for _, f := range snapshot.Files {
		if path.Base(f.Path) == "SKILL.md" || f.Path == "SKILL.md" {
			hasSkillMD = true
			break
		}
	}
	if !hasSkillMD {
		return nil, fmt.Errorf("snapshot from %s missing SKILL.md for '%s'", r.name, meta.Name)
	}

	r.logger.Info("skill snapshot downloaded",
		zap.String("name", meta.Name),
		zap.String("source", r.name),
		zap.Int("files", len(snapshot.Files)),
		zap.String("hash", snapshot.Hash),
	)

	return content, nil
}

// enrichMeta fetches the skill snapshot to extract description and version
// from the SKILL.md frontmatter. This is a best-effort enrichment —
// failures are logged but don't cause errors.
func (r *SkillsShRegistry) enrichMeta(ctx context.Context, meta *SkillMeta) {
	downloadURL := meta.DownloadURL
	if downloadURL == "" {
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return
	}
	r.setHeaders(req)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		r.logger.Debug("failed to enrich skill meta", zap.String("name", meta.Name), zap.Error(err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	var snapshot skillsshDownloadResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, skillsshMaxBody)).Decode(&snapshot); err != nil {
		return
	}

	// Find SKILL.md and extract frontmatter
	for _, f := range snapshot.Files {
		if path.Base(f.Path) == "SKILL.md" || f.Path == "SKILL.md" {
			r.parseFrontmatterIntoMeta(meta, f.Contents)
			break
		}
	}
}

// parseFrontmatterIntoMeta extracts description, version, and other fields
// from SKILL.md YAML frontmatter into the SkillMeta struct.
func (r *SkillsShRegistry) parseFrontmatterIntoMeta(meta *SkillMeta, content string) {
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

		key, val := parseFrontmatterLine(trimmed)
		if key == "" {
			continue
		}

		switch key {
		case "description":
			if meta.Description == "" {
				meta.Description = val
			}
		case "name":
			// Don't overwrite; search API name is canonical
		case "license":
			// Store in tags for display
			if val != "" {
				meta.Tags = appendUnique(meta.Tags, "license:"+val)
			}
		case "compatibility":
			if val != "" {
				meta.Tags = appendUnique(meta.Tags, "compat:"+val)
			}
		}
	}
}

// parseSkillRef parses a skill reference into skill name and optional owner/repo.
// Supported formats:
//   - "frontend-design"                     -> ("frontend-design", "")
//   - "anthropics/skills/frontend-design"    -> ("frontend-design", "anthropics/skills")
//   - "anthropics/skills:frontend-design"    -> ("frontend-design", "anthropics/skills")
func (r *SkillsShRegistry) parseSkillRef(ref string) (skillName string, ownerRepo string) {
	// Format: owner/repo:skill-name
	if idx := strings.LastIndex(ref, ":"); idx > 0 {
		ownerRepo = ref[:idx]
		skillName = ref[idx+1:]
		return
	}

	// Format: owner/repo/skill-name (3 segments)
	parts := strings.Split(ref, "/")
	if len(parts) >= 3 {
		skillName = parts[len(parts)-1]
		ownerRepo = strings.Join(parts[:len(parts)-1], "/")
		return
	}

	// Format: plain skill name (or owner/repo with no skill specified)
	if len(parts) == 2 {
		// Could be "owner/repo" — treat as search query
		skillName = ref
		return
	}

	skillName = ref
	return
}

// extractSource extracts the "owner/repo" part from a full skill ID.
// e.g. "anthropics/skills/frontend-design" -> "anthropics/skills"
func (r *SkillsShRegistry) extractSource(fullID string) string {
	parts := strings.Split(fullID, "/")
	if len(parts) >= 3 {
		return strings.Join(parts[:len(parts)-1], "/")
	}
	return ""
}

// buildDownloadURL constructs the download API URL for a skill.
func (r *SkillsShRegistry) buildDownloadURL(source string, skillID string) string {
	// source = "anthropics/skills", skillID = "frontend-design"
	// -> /api/download/anthropics/skills/frontend-design
	slug := toSkillSlug(skillID)
	return fmt.Sprintf("%s%s/%s/%s", r.baseURL, skillsshDownloadPath, source, slug)
}

func (r *SkillsShRegistry) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", skillsshUserAgent)
	req.Header.Set("Accept", "application/json")
}

// --- Utility functions ---

// toSkillSlug converts a skill name to URL-safe slug.
// Lowercases, normalizes whitespace to hyphens, removes special characters.
// Mirrors the behavior of skills.sh's toSkillSlug() in blob.ts.
func toSkillSlug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, " ", "-")

	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}

	result := b.String()
	// Clean up consecutive hyphens
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	result = strings.Trim(result, "-")
	return result
}

// parseFrontmatterLine splits a YAML frontmatter line into key and value.
func parseFrontmatterLine(line string) (string, string) {
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return "", ""
	}
	key := strings.TrimSpace(line[:idx])
	val := strings.TrimSpace(line[idx+1:])
	val = strings.Trim(val, "\"'")
	return key, val
}

// appendUnique appends a value to a slice only if it doesn't already exist.
func appendUnique(slice []string, val string) []string {
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}

// FormatInstallCount formats a large number into a human-readable string.
// e.g. 280900 -> "280.9K", 1500000 -> "1.5M"
func FormatInstallCount(count int) string {
	if count < 1000 {
		return fmt.Sprintf("%d", count)
	}
	if count < 1_000_000 {
		k := float64(count) / 1000.0
		if k >= 100 {
			return fmt.Sprintf("%.0fK", k)
		}
		if k >= 10 {
			return fmt.Sprintf("%.1fK", k)
		}
		return fmt.Sprintf("%.1fK", k)
	}
	m := float64(count) / 1_000_000.0
	return fmt.Sprintf("%.1fM", m)
}

// IsSkillsShSource checks if a registry name corresponds to skills.sh.
func IsSkillsShSource(registryName string) bool {
	return registryName == "skills.sh" || registryName == "skillssh"
}

// --- Security Audit types and functions ---

// PartnerAudit represents a security assessment from a single audit provider.
type PartnerAudit struct {
	Risk       string `json:"risk"`       // "safe", "low", "medium", "high", "critical", "unknown"
	Alerts     int    `json:"alerts"`     // number of alerts (Socket)
	Score      int    `json:"score"`      // numeric score if available
	AnalyzedAt string `json:"analyzedAt"` // ISO 8601 timestamp
}

// SkillAuditData holds audit results from all providers for a single skill.
type SkillAuditData struct {
	ATH    *PartnerAudit `json:"ath"`    // Gen Agent Trust Hub
	Socket *PartnerAudit `json:"socket"` // Socket
	Snyk   *PartnerAudit `json:"snyk"`   // Snyk
}

// AuditResponse maps skill slugs to their audit data.
type AuditResponse map[string]SkillAuditData

// FetchAuditData retrieves security risk assessments for skills from the
// skills.sh audit API. This calls the partner audit endpoint that aggregates
// results from Gen Agent Trust Hub, Socket, and Snyk.
//
// The call is best-effort with a short timeout (3s) — security data is
// advisory and should never block installation or info display.
//
// API: GET https://add-skill.vercel.sh/audit?source={owner/repo}&skills={slug1,slug2,...}
func FetchAuditData(ctx context.Context, source string, skillSlugs []string) (AuditResponse, error) {
	if source == "" || len(skillSlugs) == 0 {
		return nil, fmt.Errorf("source and skillSlugs are required")
	}

	// Build URL with query params
	params := url.Values{}
	params.Set("source", source)
	params.Set("skills", strings.Join(skillSlugs, ","))
	endpoint := skillsshAuditURL + "?" + params.Encode()

	// Use a short timeout — this is advisory, not critical
	auditCtx, cancel := context.WithTimeout(ctx, skillsshAuditTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(auditCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating audit request: %w", err)
	}
	req.Header.Set("User-Agent", skillsshUserAgent)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: skillsshAuditTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching audit data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("audit API returned %d", resp.StatusCode)
	}

	var auditResp AuditResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, skillsshMaxBody)).Decode(&auditResp); err != nil {
		return nil, fmt.Errorf("decoding audit response: %w", err)
	}

	return auditResp, nil
}

// FormatRiskLevel returns a human-readable label for a risk level.
func FormatRiskLevel(risk string) string {
	switch strings.ToLower(risk) {
	case "safe":
		return "Safe"
	case "low":
		return "Low Risk"
	case "medium":
		return "Med Risk"
	case "high":
		return "High Risk"
	case "critical":
		return "Critical Risk"
	default:
		return "--"
	}
}

// FormatSocketAlerts returns a human-readable label for Socket alert count.
func FormatSocketAlerts(audit *PartnerAudit) string {
	if audit == nil {
		return "--"
	}
	if audit.Alerts > 0 {
		suffix := "s"
		if audit.Alerts == 1 {
			suffix = ""
		}
		return fmt.Sprintf("%d alert%s", audit.Alerts, suffix)
	}
	return "0 alerts"
}

// ParseSnapshotFiles parses snapshot JSON into individual files.
// Returns the files list and content hash, or an error.
func ParseSnapshotFiles(data []byte) ([]SnapshotFile, string, error) {
	var resp skillsshDownloadResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, "", fmt.Errorf("parsing snapshot: %w", err)
	}

	files := make([]SnapshotFile, len(resp.Files))
	for i, f := range resp.Files {
		// Validate path — reject path traversal
		cleaned := path.Clean(f.Path)
		if strings.HasPrefix(cleaned, "..") || strings.HasPrefix(cleaned, "/") {
			return nil, "", fmt.Errorf("unsafe path in snapshot: %q", f.Path)
		}
		files[i] = SnapshotFile{
			Path:     cleaned,
			Contents: f.Contents,
		}
	}

	return files, resp.Hash, nil
}
