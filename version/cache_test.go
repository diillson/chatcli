/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package version

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// isolateHome aponta o home do usuário para um tempdir do teste, cobrindo
// Unix (HOME) e Windows (USERPROFILE).
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestReleaseCache_RoundTrip(t *testing.T) {
	isolateHome(t)

	saveReleaseCache(ReleaseInfo{TagName: "v1.161.0", TargetHash: "abcdef123456", PublishedAt: "2026-07-12T10:00:00Z"})

	got, ok := loadReleaseCache()
	assert.True(t, ok)
	assert.Equal(t, "v1.161.0", got.TagName)
	assert.Equal(t, "abcdef123456", got.TargetHash)
}

func TestReleaseCache_ExpiredAndCorrupted(t *testing.T) {
	home := isolateHome(t)
	path := filepath.Join(home, ".chatcli", "cache", "latest-release.json")
	assert.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))

	// Vencido: continua sendo o último dado conhecido — carrega para
	// exibição, mas deixa de ser fresco (obriga um novo fetch).
	stale, err := json.Marshal(releaseCacheEntry{
		FetchedAt: time.Now().UTC().Add(-releaseCacheTTL - time.Hour),
		Release:   ReleaseInfo{TagName: "v1.0.0"},
	})
	assert.NoError(t, err)
	assert.NoError(t, os.WriteFile(path, stale, 0o600))
	release, ok := loadReleaseCache()
	assert.True(t, ok, "cache vencido ainda alimenta a exibição")
	assert.Equal(t, "v1.0.0", release.TagName)
	entry, ok := loadReleaseCacheEntry()
	assert.True(t, ok)
	assert.False(t, entry.fresh(), "cache vencido deve exigir novo fetch")

	// Corrompido: JSON truncado.
	assert.NoError(t, os.WriteFile(path, []byte(`{"fetched_at":`), 0o600))
	_, ok = loadReleaseCache()
	assert.False(t, ok, "cache corrompido deve contar como ausente")
}

func TestOfflineReport_EnrichesFromCache(t *testing.T) {
	isolateHome(t)

	originalBuildImpl := GetBuildInfoImpl
	GetBuildInfoImpl = func() (string, string, string) {
		return "1.161.0", "unknown", "unknown"
	}
	defer func() { GetBuildInfoImpl = originalBuildImpl }()

	// Sem cache: só o build info local.
	rep := OfflineReport()
	assert.Equal(t, "unknown", rep.Current.CommitHash)
	assert.Empty(t, rep.Latest)

	// Com cache fresco da MESMA versão: hash e data preenchidos, sem rede.
	saveReleaseCache(ReleaseInfo{TagName: "v1.161.0", TargetHash: "0123456789abcdef", PublishedAt: "2026-07-12T10:00:00Z"})
	rep = OfflineReport()
	assert.Equal(t, "0123456789ab", rep.Current.CommitHash)
	assert.Equal(t, "2026-07-12 10:00:00", rep.Current.BuildDate)
	assert.Equal(t, "1.161.0", rep.Latest)
	assert.False(t, rep.NeedsUpdate)
}

func TestOfflineReport_NoEnrichmentOnVersionMismatch(t *testing.T) {
	isolateHome(t)

	originalBuildImpl := GetBuildInfoImpl
	GetBuildInfoImpl = func() (string, string, string) {
		return "1.160.0", "unknown", "unknown"
	}
	defer func() { GetBuildInfoImpl = originalBuildImpl }()

	saveReleaseCache(ReleaseInfo{TagName: "v1.161.0", TargetHash: "0123456789abcdef", PublishedAt: "2026-07-12T10:00:00Z"})
	rep := OfflineReport()
	assert.Equal(t, "unknown", rep.Current.CommitHash, "metadados de outra release não podem contaminar")
	assert.True(t, rep.NeedsUpdate)
}

func TestGetReport_PopulatesCache(t *testing.T) {
	isolateHome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tag_name": "v1.161.0", "target_commitish": "feedfacecafe", "published_at": "2026-07-12T10:00:00Z"}`))
	}))
	defer server.Close()
	t.Setenv("CHATCLI_LATEST_VERSION_URL", server.URL)

	_ = GetReport(t.Context())

	got, ok := loadReleaseCache()
	assert.True(t, ok, "GetReport deve semear o cache após fetch bem-sucedido")
	assert.Equal(t, "v1.161.0", got.TagName)
}

func TestRefreshReleaseCacheIfStale(t *testing.T) {
	isolateHome(t)

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tag_name": "v1.161.0"}`))
	}))
	defer server.Close()
	t.Setenv("CHATCLI_LATEST_VERSION_URL", server.URL)

	// Opt-out respeitado: nenhuma chamada.
	t.Setenv("CHATCLI_DISABLE_VERSION_CHECK", "true")
	RefreshReleaseCacheIfStale(t.Context())
	assert.EqualValues(t, 0, calls.Load())
	t.Setenv("CHATCLI_DISABLE_VERSION_CHECK", "")

	// Sem cache: busca uma vez.
	RefreshReleaseCacheIfStale(t.Context())
	assert.EqualValues(t, 1, calls.Load())

	// Cache fresco: não busca de novo.
	RefreshReleaseCacheIfStale(t.Context())
	assert.EqualValues(t, 1, calls.Load())

	// Cache vencido: exibição continua servida, mas o refresh busca de novo.
	saveStaleReleaseCache(t, ReleaseInfo{TagName: "v1.161.0"})
	RefreshReleaseCacheIfStale(t.Context())
	assert.EqualValues(t, 2, calls.Load())
}

// saveStaleReleaseCache grava uma entrada de cache já vencida (além do TTL).
func saveStaleReleaseCache(t *testing.T, release ReleaseInfo) {
	t.Helper()
	home, err := os.UserHomeDir()
	assert.NoError(t, err)
	path := filepath.Join(home, ".chatcli", "cache", "latest-release.json")
	assert.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	data, err := json.Marshal(releaseCacheEntry{
		FetchedAt: time.Now().UTC().Add(-releaseCacheTTL - time.Hour),
		Release:   release,
	})
	assert.NoError(t, err)
	assert.NoError(t, os.WriteFile(path, data, 0o600))
}

func TestOfflineReport_ExpiredCacheStillReportsUpdate(t *testing.T) {
	isolateHome(t)

	originalBuildImpl := GetBuildInfoImpl
	GetBuildInfoImpl = func() (string, string, string) {
		return "1.160.0", "unknown", "unknown"
	}
	defer func() { GetBuildInfoImpl = originalBuildImpl }()

	saveStaleReleaseCache(t, ReleaseInfo{
		TagName: "v1.161.0",
		Body:    "### Features\n\n* nova feature",
		HTMLURL: "https://github.com/diillson/chatcli/releases/tag/v1.161.0",
	})

	rep := OfflineReport()
	assert.True(t, rep.NeedsUpdate, "cache vencido ainda é o último dado conhecido")
	assert.Equal(t, "1.161.0", rep.Latest)
	assert.Contains(t, rep.Notes, "nova feature")
	assert.Equal(t, "https://github.com/diillson/chatcli/releases/tag/v1.161.0", rep.ReleaseURL)
}
