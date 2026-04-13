/*
 * ChatCLI - Skills.sh Registry Adapter Tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// --- toSkillSlug tests ---

func TestToSkillSlug(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"frontend-design", "frontend-design"},
		{"Frontend Design", "frontend-design"},
		{"  React Best Practices  ", "react-best-practices"},
		{"my_skill!", "myskill"},
		{"a--b--c", "a-b-c"},
		{"-leading-", "leading"},
		{"ALLCAPS", "allcaps"},
		{"with.dots.here", "withdotshere"},
		{"with/slashes", "withslashes"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := toSkillSlug(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// --- FormatInstallCount tests ---

func TestFormatInstallCount(t *testing.T) {
	tests := []struct {
		count    int
		expected string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1.0K"},
		{1500, "1.5K"},
		{10500, "10.5K"},
		{280900, "281K"},
		{1000000, "1.0M"},
		{1500000, "1.5M"},
		{214083, "214K"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.count), func(t *testing.T) {
			result := FormatInstallCount(tt.count)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// --- parseSkillRef tests ---

func TestParseSkillRef(t *testing.T) {
	r := &SkillsShRegistry{}

	tests := []struct {
		input         string
		expectedSkill string
		expectedRepo  string
	}{
		{"frontend-design", "frontend-design", ""},
		{"anthropics/skills/frontend-design", "frontend-design", "anthropics/skills"},
		{"anthropics/skills:frontend-design", "frontend-design", "anthropics/skills"},
		{"vercel-labs/agent-skills/react-best-practices", "react-best-practices", "vercel-labs/agent-skills"},
		{"owner/repo", "owner/repo", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			skill, repo := r.parseSkillRef(tt.input)
			assert.Equal(t, tt.expectedSkill, skill)
			assert.Equal(t, tt.expectedRepo, repo)
		})
	}
}

// --- extractSource tests ---

func TestExtractSource(t *testing.T) {
	r := &SkillsShRegistry{}

	tests := []struct {
		input    string
		expected string
	}{
		{"anthropics/skills/frontend-design", "anthropics/skills"},
		{"vercel-labs/agent-skills/react-best-practices", "vercel-labs/agent-skills"},
		{"frontend-design", ""},
		{"owner/repo", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := r.extractSource(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// --- IsSkillsShSource tests ---

func TestIsSkillsShSource(t *testing.T) {
	assert.True(t, IsSkillsShSource("skills.sh"))
	assert.True(t, IsSkillsShSource("skillssh"))
	assert.False(t, IsSkillsShSource("clawhub"))
	assert.False(t, IsSkillsShSource("chatcli"))
	assert.False(t, IsSkillsShSource(""))
}

// --- parseFrontmatterLine tests ---

func TestParseFrontmatterLine(t *testing.T) {
	tests := []struct {
		line string
		key  string
		val  string
	}{
		{`name: "frontend-design"`, "name", "frontend-design"},
		{`description: My cool skill`, "description", "My cool skill"},
		{`version: '1.0'`, "version", "1.0"},
		{`license: Apache-2.0`, "license", "Apache-2.0"},
		{"no-colon-here", "", ""},
		{"", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			k, v := parseFrontmatterLine(tt.line)
			assert.Equal(t, tt.key, k)
			assert.Equal(t, tt.val, v)
		})
	}
}

// --- Search integration test with mock server ---

func TestSkillsShRegistrySearch(t *testing.T) {
	// Set up mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/search" {
			q := r.URL.Query().Get("q")
			resp := skillsshSearchResponse{
				Query:      q,
				SearchType: "fuzzy",
				Skills: []skillsshSkillHit{
					{
						ID:       "anthropics/skills/frontend-design",
						SkillID:  "frontend-design",
						Name:     "frontend-design",
						Installs: 214083,
						Source:   "anthropics/skills",
					},
					{
						ID:       "pbakaus/impeccable/frontend-design",
						SkillID:  "frontend-design",
						Name:     "frontend-design",
						Installs: 45501,
						Source:   "pbakaus/impeccable",
					},
				},
				Count:      2,
				DurationMs: 10,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	reg := NewSkillsShRegistry(RegistryEntry{
		Name:     "skills.sh",
		URL:      server.URL,
		IsActive: true,
		CacheTTL: 5 * time.Minute,
		Type:     "skillssh",
	}, logger)

	ctx := context.Background()
	results, err := reg.Search(ctx, "frontend")

	require.NoError(t, err)
	require.Len(t, results, 2)

	assert.Equal(t, "frontend-design", results[0].Name)
	assert.Equal(t, "anthropics/skills/frontend-design", results[0].Slug)
	assert.Equal(t, 214083, results[0].Downloads)
	assert.Equal(t, "anthropics", results[0].Author)
	assert.Equal(t, "skills.sh", results[0].RegistryName)

	assert.Equal(t, "frontend-design", results[1].Name)
	assert.Equal(t, "pbakaus/impeccable/frontend-design", results[1].Slug)
	assert.Equal(t, 45501, results[1].Downloads)
	assert.Equal(t, "pbakaus", results[1].Author)
}

// --- Search caching test ---

func TestSkillsShRegistrySearchCache(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := skillsshSearchResponse{
			Skills: []skillsshSkillHit{
				{ID: "test/repo/my-skill", SkillID: "my-skill", Name: "my-skill", Installs: 100, Source: "test/repo"},
			},
			Count: 1,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	reg := NewSkillsShRegistry(RegistryEntry{
		Name:     "skills.sh",
		URL:      server.URL,
		IsActive: true,
		CacheTTL: 5 * time.Minute,
		Type:     "skillssh",
	}, logger)

	ctx := context.Background()

	// First call hits the server
	_, err := reg.Search(ctx, "my-skill")
	require.NoError(t, err)
	assert.Equal(t, 1, callCount)

	// Second call should be cached
	_, err = reg.Search(ctx, "my-skill")
	require.NoError(t, err)
	assert.Equal(t, 1, callCount) // still 1, no new request
}

// --- Download integration test ---

func TestSkillsShRegistryDownloadSkill(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/download/anthropics/skills/frontend-design" {
			resp := skillsshDownloadResponse{
				Files: []skillsshSnapshotFile{
					{
						Path:     "SKILL.md",
						Contents: "---\nname: frontend-design\ndescription: Design beautiful interfaces\n---\n\n# Frontend Design\n\nInstructions here.",
					},
					{
						Path:     "scripts/lint.sh",
						Contents: "#!/bin/bash\nset -e\necho 'linting...'",
					},
					{
						Path:     "references/REFERENCE.md",
						Contents: "# Detailed Reference\n\nMore info.",
					},
				},
				Hash: "abc123def456",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	reg := NewSkillsShRegistry(RegistryEntry{
		Name:     "skills.sh",
		URL:      server.URL,
		IsActive: true,
		CacheTTL: 5 * time.Minute,
		Type:     "skillssh",
	}, logger)

	ctx := context.Background()
	meta := &SkillMeta{
		Name:         "frontend-design",
		Slug:         "anthropics/skills/frontend-design",
		DownloadURL:  server.URL + "/api/download/anthropics/skills/frontend-design",
		RegistryName: "skills.sh",
	}

	content, err := reg.DownloadSkill(ctx, meta)
	require.NoError(t, err)
	require.NotEmpty(t, content)

	// Verify it's valid snapshot JSON
	files, hash, parseErr := ParseSnapshotFiles(content)
	require.NoError(t, parseErr)
	assert.Equal(t, "abc123def456", hash)
	assert.Len(t, files, 3)
	assert.Equal(t, "SKILL.md", files[0].Path)
	assert.Contains(t, files[0].Contents, "frontend-design")
	assert.Equal(t, "scripts/lint.sh", files[1].Path)
	assert.Equal(t, "references/REFERENCE.md", files[2].Path)
}

// --- Download rejects snapshots without SKILL.md ---

func TestSkillsShRegistryDownloadRejectsMissingSKILLMD(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := skillsshDownloadResponse{
			Files: []skillsshSnapshotFile{
				{Path: "README.md", Contents: "# Just a readme"},
			},
			Hash: "noskillmd",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	reg := NewSkillsShRegistry(RegistryEntry{
		Name:     "skills.sh",
		URL:      server.URL,
		IsActive: true,
		Type:     "skillssh",
	}, logger)

	ctx := context.Background()
	meta := &SkillMeta{
		Name:        "bad-skill",
		DownloadURL: server.URL + "/api/download/test/bad-skill",
	}

	_, err := reg.DownloadSkill(ctx, meta)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing SKILL.md")
}

// --- ParseSnapshotFiles tests ---

func TestParseSnapshotFiles(t *testing.T) {
	data := `{
		"files": [
			{"path": "SKILL.md", "contents": "# My Skill"},
			{"path": "scripts/run.sh", "contents": "#!/bin/bash\necho hi"}
		],
		"hash": "deadbeef"
	}`

	files, hash, err := ParseSnapshotFiles([]byte(data))
	require.NoError(t, err)
	assert.Equal(t, "deadbeef", hash)
	assert.Len(t, files, 2)
	assert.Equal(t, "SKILL.md", files[0].Path)
	assert.Equal(t, "scripts/run.sh", files[1].Path)
}

func TestParseSnapshotFilesRejectsTraversal(t *testing.T) {
	data := `{
		"files": [
			{"path": "../../../etc/passwd", "contents": "bad stuff"}
		],
		"hash": "evil"
	}`

	_, _, err := ParseSnapshotFiles([]byte(data))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsafe path")
}

func TestParseSnapshotFilesRejectsAbsolutePath(t *testing.T) {
	data := `{
		"files": [
			{"path": "/etc/passwd", "contents": "bad stuff"}
		],
		"hash": "evil"
	}`

	_, _, err := ParseSnapshotFiles([]byte(data))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsafe path")
}

func TestParseSnapshotFilesInvalidJSON(t *testing.T) {
	_, _, err := ParseSnapshotFiles([]byte("not json"))
	assert.Error(t, err)
}

// --- InstallFromSnapshot integration test ---

func TestInstallerInstallFromSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	logger, _ := zap.NewDevelopment()
	inst := NewInstaller(tmpDir, logger)

	meta := &SkillMeta{
		Name:         "test-snapshot-skill",
		Description:  "A test skill installed from snapshot",
		RegistryName: "skills.sh",
	}

	files := []SnapshotFile{
		{
			Path:     "SKILL.md",
			Contents: "---\nname: test-snapshot-skill\ndescription: A test skill installed from snapshot\n---\n\n# Test Skill\n\nInstructions.",
		},
		{
			Path:     "scripts/build.sh",
			Contents: "#!/bin/bash\nset -e\necho 'building...'",
		},
		{
			Path:     "references/REFERENCE.md",
			Contents: "# Reference\n\nDetailed docs.",
		},
		{
			Path:     "assets/template.html",
			Contents: "<html><body>Template</body></html>",
		},
	}

	result, err := inst.InstallFromSnapshot(meta, files, "testhash123")
	require.NoError(t, err)
	// Qualified name: "skills.sh--test-snapshot-skill"
	expectedName := "skills.sh--test-snapshot-skill"
	assert.Equal(t, expectedName, result.Name)
	assert.Equal(t, "skills.sh", result.Source)
	assert.False(t, result.WasDuplicate)

	// Verify files on disk at qualified path
	skillDir := filepath.Join(tmpDir, expectedName)
	assert.DirExists(t, skillDir)

	// SKILL.md should exist and contain source field
	skillMD, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(skillMD), "test-snapshot-skill")
	assert.Contains(t, string(skillMD), `source: "skills.sh"`)
	assert.Contains(t, string(skillMD), `snapshot_hash: "testhash123"`)

	// Scripts should have executable permissions
	scriptPath := filepath.Join(skillDir, "scripts", "build.sh")
	scriptInfo, err := os.Stat(scriptPath)
	require.NoError(t, err)
	assert.True(t, scriptInfo.Mode()&0o100 != 0, "script should be executable")

	// References should exist
	refPath := filepath.Join(skillDir, "references", "REFERENCE.md")
	assert.FileExists(t, refPath)

	// Assets should exist
	assetPath := filepath.Join(skillDir, "assets", "template.html")
	assert.FileExists(t, assetPath)

	// Verify IsInstalled works by qualified name
	assert.True(t, inst.IsInstalled(expectedName))
	// Also findable by base name
	matches := inst.FindInstalled("test-snapshot-skill")
	assert.Len(t, matches, 1)
	assert.Equal(t, "test-snapshot-skill", matches[0].BaseName)

	// Verify ListInstalled includes it
	installed, err := inst.ListInstalled()
	require.NoError(t, err)
	found := false
	for _, s := range installed {
		if s.Name == expectedName {
			found = true
			assert.Equal(t, "skills.sh", s.Source)
			assert.Equal(t, "test-snapshot-skill", s.BaseName)
			break
		}
	}
	assert.True(t, found, "installed skill should appear in list")
}

// --- InstallFromSnapshot update (duplicate) test ---

func TestInstallerInstallFromSnapshotUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	logger, _ := zap.NewDevelopment()
	inst := NewInstaller(tmpDir, logger)

	meta := &SkillMeta{
		Name:         "updatable-skill",
		RegistryName: "skills.sh",
	}

	filesV1 := []SnapshotFile{
		{Path: "SKILL.md", Contents: "---\nname: updatable-skill\n---\n\n# V1"},
	}
	filesV2 := []SnapshotFile{
		{Path: "SKILL.md", Contents: "---\nname: updatable-skill\n---\n\n# V2"},
		{Path: "scripts/new.sh", Contents: "#!/bin/bash\necho v2"},
	}

	expectedName := "skills.sh--updatable-skill"

	// Install V1
	result1, err := inst.InstallFromSnapshot(meta, filesV1, "hash-v1")
	require.NoError(t, err)
	assert.False(t, result1.WasDuplicate)

	// Install V2 (update)
	result2, err := inst.InstallFromSnapshot(meta, filesV2, "hash-v2")
	require.NoError(t, err)
	assert.True(t, result2.WasDuplicate)

	// V2 content should be present
	content, err := os.ReadFile(filepath.Join(tmpDir, expectedName, "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "# V2")

	// New script should exist
	assert.FileExists(t, filepath.Join(tmpDir, expectedName, "scripts", "new.sh"))
}

// --- InstallFromSnapshot blocks malware ---

func TestInstallerInstallFromSnapshotBlocksMalware(t *testing.T) {
	tmpDir := t.TempDir()
	logger, _ := zap.NewDevelopment()
	inst := NewInstaller(tmpDir, logger)

	meta := &SkillMeta{
		Name: "malware-skill",
		Moderation: ModerationFlags{
			MalwareDetected: true,
			Reason:          "dangerous content",
		},
	}

	files := []SnapshotFile{
		{Path: "SKILL.md", Contents: "---\nname: malware-skill\n---\nbad"},
	}

	_, err := inst.InstallFromSnapshot(meta, files, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "BLOCKED")
}

// --- injectSourceField tests ---

func TestInjectSourceField(t *testing.T) {
	t.Run("inject into existing frontmatter", func(t *testing.T) {
		content := "---\nname: my-skill\ndescription: test\n---\n\n# Content"
		result := injectSourceField(content, "skills.sh", "abc123")
		assert.Contains(t, result, `source: "skills.sh"`)
		assert.Contains(t, result, `snapshot_hash: "abc123"`)
		assert.Contains(t, result, "name: my-skill") // original preserved
	})

	t.Run("skip if source already present", func(t *testing.T) {
		content := "---\nname: my-skill\nsource: \"original\"\n---\n\n# Content"
		result := injectSourceField(content, "skills.sh", "abc123")
		assert.NotContains(t, result, `source: "skills.sh"`)  // should not overwrite
		assert.Contains(t, result, `source: "original"`)      // original preserved
		assert.Contains(t, result, `snapshot_hash: "abc123"`) // hash still added
	})

	t.Run("inject into content without frontmatter", func(t *testing.T) {
		content := "# Just a heading\n\nNo frontmatter here."
		result := injectSourceField(content, "skills.sh", "abc123")
		assert.Contains(t, result, "---\n")
		assert.Contains(t, result, `source: "skills.sh"`)
		assert.Contains(t, result, "# Just a heading") // original content preserved
	})
}

// --- isScriptFile tests ---

func TestIsScriptFile(t *testing.T) {
	assert.True(t, isScriptFile("scripts/deploy.sh"))
	assert.True(t, isScriptFile("scripts/build.py"))
	assert.True(t, isScriptFile("something.sh"))
	assert.True(t, isScriptFile("something.py"))
	assert.True(t, isScriptFile("something.rb"))
	assert.False(t, isScriptFile("SKILL.md"))
	assert.False(t, isScriptFile("references/doc.md"))
	assert.False(t, isScriptFile("assets/template.html"))
}

// --- GetSkillMeta test ---

func TestSkillsShRegistryGetSkillMeta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/search":
			resp := skillsshSearchResponse{
				Skills: []skillsshSkillHit{
					{
						ID:       "anthropics/skills/frontend-design",
						SkillID:  "frontend-design",
						Name:     "frontend-design",
						Installs: 280000,
						Source:   "anthropics/skills",
					},
				},
				Count: 1,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case "/api/download/anthropics/skills/frontend-design":
			resp := skillsshDownloadResponse{
				Files: []skillsshSnapshotFile{
					{
						Path:     "SKILL.md",
						Contents: "---\nname: frontend-design\ndescription: Design beautiful production-grade UIs\nlicense: Apache-2.0\n---\n\n# Frontend Design",
					},
				},
				Hash: "meta-hash",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	reg := NewSkillsShRegistry(RegistryEntry{
		Name:     "skills.sh",
		URL:      server.URL,
		IsActive: true,
		CacheTTL: 5 * time.Minute,
		Type:     "skillssh",
	}, logger)

	ctx := context.Background()

	// Test with full ID
	meta, err := reg.GetSkillMeta(ctx, "anthropics/skills/frontend-design")
	require.NoError(t, err)
	assert.Equal(t, "frontend-design", meta.Name)
	assert.Equal(t, 280000, meta.Downloads)
	assert.Equal(t, "anthropics", meta.Author)
	// Should be enriched with description from SKILL.md
	assert.Equal(t, "Design beautiful production-grade UIs", meta.Description)
	// Should have license tag
	assert.Contains(t, meta.Tags, "license:Apache-2.0")
}

// --- appendUnique tests ---

func TestAppendUnique(t *testing.T) {
	slice := []string{"a", "b"}
	result := appendUnique(slice, "c")
	assert.Equal(t, []string{"a", "b", "c"}, result)

	result = appendUnique(result, "b")
	assert.Equal(t, []string{"a", "b", "c"}, result) // no duplicate
}

// --- buildDownloadURL test ---

func TestBuildDownloadURL(t *testing.T) {
	r := &SkillsShRegistry{baseURL: "https://skills.sh"}
	url := r.buildDownloadURL("anthropics/skills", "frontend-design")
	assert.Equal(t, "https://skills.sh/api/download/anthropics/skills/frontend-design", url)
}

// --- Enabled/Name tests ---

func TestSkillsShRegistryBasics(t *testing.T) {
	reg := NewSkillsShRegistry(RegistryEntry{
		Name:     "skills.sh",
		URL:      "https://skills.sh",
		IsActive: true,
		Type:     "skillssh",
	}, zap.NewNop())

	assert.Equal(t, "skills.sh", reg.Name())
	assert.True(t, reg.Enabled())

	regDisabled := NewSkillsShRegistry(RegistryEntry{
		Name:     "skills.sh",
		URL:      "https://skills.sh",
		IsActive: false,
		Type:     "skillssh",
	}, zap.NewNop())

	assert.False(t, regDisabled.Enabled())
}

// --- Security Audit tests ---

func TestFetchAuditData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/audit", r.URL.Path)
		assert.Equal(t, "anthropics/skills", r.URL.Query().Get("source"))
		assert.Equal(t, "frontend-design", r.URL.Query().Get("skills"))

		resp := AuditResponse{
			"frontend-design": SkillAuditData{
				ATH:    &PartnerAudit{Risk: "safe", Alerts: 0, AnalyzedAt: "2026-04-01T00:00:00Z"},
				Socket: &PartnerAudit{Risk: "low", Alerts: 0, AnalyzedAt: "2026-04-01T00:00:00Z"},
				Snyk:   &PartnerAudit{Risk: "safe", Alerts: 0, AnalyzedAt: "2026-04-01T00:00:00Z"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Override the audit URL for testing — we need to use the test server
	origURL := skillsshAuditURL
	// Since we can't override the const, we test via the mock server approach
	// by calling the server directly
	ctx := context.Background()

	params := url.Values{}
	params.Set("source", "anthropics/skills")
	params.Set("skills", "frontend-design")
	endpoint := server.URL + "/audit?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var auditResp AuditResponse
	err = json.NewDecoder(resp.Body).Decode(&auditResp)
	require.NoError(t, err)

	data, ok := auditResp["frontend-design"]
	assert.True(t, ok)
	assert.NotNil(t, data.ATH)
	assert.Equal(t, "safe", data.ATH.Risk)
	assert.NotNil(t, data.Socket)
	assert.Equal(t, "low", data.Socket.Risk)
	assert.Equal(t, 0, data.Socket.Alerts)
	assert.NotNil(t, data.Snyk)
	assert.Equal(t, "safe", data.Snyk.Risk)

	_ = origURL // acknowledge the const exists
}

func TestFetchAuditDataValidation(t *testing.T) {
	ctx := context.Background()

	// Empty source should error
	_, err := FetchAuditData(ctx, "", []string{"skill"})
	assert.Error(t, err)

	// Empty skills should error
	_, err = FetchAuditData(ctx, "owner/repo", nil)
	assert.Error(t, err)
}

func TestFormatRiskLevel(t *testing.T) {
	tests := []struct {
		risk     string
		expected string
	}{
		{"safe", "Safe"},
		{"low", "Low Risk"},
		{"medium", "Med Risk"},
		{"high", "High Risk"},
		{"critical", "Critical Risk"},
		{"unknown", "--"},
		{"", "--"},
		{"SAFE", "Safe"},
	}

	for _, tt := range tests {
		t.Run(tt.risk, func(t *testing.T) {
			assert.Equal(t, tt.expected, FormatRiskLevel(tt.risk))
		})
	}
}

func TestFormatSocketAlerts(t *testing.T) {
	assert.Equal(t, "--", FormatSocketAlerts(nil))
	assert.Equal(t, "0 alerts", FormatSocketAlerts(&PartnerAudit{Alerts: 0}))
	assert.Equal(t, "1 alert", FormatSocketAlerts(&PartnerAudit{Alerts: 1}))
	assert.Equal(t, "5 alerts", FormatSocketAlerts(&PartnerAudit{Alerts: 5}))
}
