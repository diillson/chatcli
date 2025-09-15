package version

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNeedsUpdate(t *testing.T) {
	testCases := []struct {
		name     string
		current  string
		latest   string
		expected bool
	}{
		{"Major update needed", "1.0.0", "2.0.0", true},
		{"Minor update needed", "1.1.0", "1.2.0", true},
		{"Patch update needed", "1.1.1", "1.1.2", true},
		{"No update needed (same)", "1.2.3", "1.2.3", false},
		{"No update needed (older)", "2.0.0", "1.9.9", false},
		{"With 'v' prefix", "v1.2.0", "v1.3.0", true},
		{"Dev version", "dev", "1.0.0", false},
		{"Unknown version", "unknown", "1.0.0", false},
		{"Pseudo-version", "v0.0.0-20240101-abcdef", "1.0.0", false},
		// *** CORREÇÃO DA EXPECTATIVA E NOVOS CASOS ***
		{"Current is pre-release, needs update", "1.2.3-alpha", "1.2.3", true},
		{"Current is pre-release, latest is newer pre-release", "1.2.3-alpha", "1.2.3-beta", true},
		{"Current is stable, latest is pre-release (no update)", "1.2.3", "1.2.3-beta", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := NeedsUpdate(tc.current, tc.latest)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// stripANSI remove códigos ANSI
func stripANSI(s string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return re.ReplaceAllString(s, "")
}

// normalizeSpaces remove espaços extras
func normalizeSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func TestFormatVersionInfo(t *testing.T) {
	info := VersionInfo{
		Version:    "1.25.0",
		CommitHash: "abc1234",
		BuildDate:  "2024-09-15",
	}

	testCases := []struct {
		name      string
		latest    string
		hasUpdate bool
		checkErr  error
		expectStr string // String a buscar (flexível)
	}{
		{"With update available", "1.26.0", true, nil, "Disponível! Atualize"},
		{"No update", "1.25.0", false, nil, "Você está na versão mais recente."},
		{"With error", "", false, errors.New("network error"), "Não foi possível verificar: network error"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			output := FormatVersionInfo(info, tc.latest, tc.hasUpdate, tc.checkErr)
			cleanOutput := stripANSI(output)
			normalized := normalizeSpaces(cleanOutput)

			// Asserções flexíveis: verifica conteúdo chave sem espaços exatos
			assert.Contains(t, normalized, "Versão: 1.25.0")
			assert.Contains(t, normalized, tc.expectStr)
		})
	}
}

func TestCheckLatestVersionWithContext_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tag_name": "v1.26.0"}`))
	}))
	defer server.Close()

	// Salva implementações originais
	originalCheckImpl := CheckLatestVersionImpl
	originalBuildImpl := GetBuildInfoImpl

	// Mock GetBuildInfoImpl para retornar versão comparável (não "dev")
	GetBuildInfoImpl = func() (string, string, string) {
		return "1.25.0", "abc1234", "2024-09-15"
	}

	// Mock CheckLatestVersionImpl para usar servidor de teste
	CheckLatestVersionImpl = func(ctx context.Context) (string, bool, error) {
		client := &http.Client{Timeout: 5 * time.Second}
		req, err := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
		if err != nil {
			return "", false, err
		}
		req.Header.Set("User-Agent", "ChatCLI-Version-Checker")

		resp, err := client.Do(req)
		if err != nil {
			return "", false, err
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", false, err
		}

		var releaseInfo struct {
			TagName string `json:"tag_name"`
		}
		if err := json.Unmarshal(body, &releaseInfo); err != nil {
			return "", false, err
		}

		latestVersion := strings.TrimPrefix(releaseInfo.TagName, "v")
		currentVersionFull, _, _ := GetBuildInfo()
		currentVersionBase := ExtractBaseVersion(currentVersionFull)
		needsUpdate := NeedsUpdate(currentVersionBase, latestVersion)

		return latestVersion, needsUpdate, nil
	}
	defer func() {
		CheckLatestVersionImpl = originalCheckImpl
		GetBuildInfoImpl = originalBuildImpl
	}()

	ctx := context.Background()
	latest, hasUpdate, err := CheckLatestVersionWithContext(ctx)

	assert.NoError(t, err)
	assert.Equal(t, "1.26.0", latest)
	assert.True(t, hasUpdate)
}

func TestCheckLatestVersionWithContext_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // Simula delay
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tag_name": "v1.0.0"}`))
	}))
	defer server.Close()

	// Salva original
	originalImpl := CheckLatestVersionImpl

	// Override com implementação que usa o servidor de teste (com delay)
	CheckLatestVersionImpl = func(ctx context.Context) (string, bool, error) {
		client := &http.Client{Timeout: 5 * time.Second}
		req, err := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
		if err != nil {
			return "", false, err
		}
		req.Header.Set("User-Agent", "ChatCLI-Version-Checker")

		resp, err := client.Do(req)
		if err != nil {
			return "", false, err
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", false, err
		}

		var releaseInfo struct {
			TagName string `json:"tag_name"`
		}
		if err := json.Unmarshal(body, &releaseInfo); err != nil {
			return "", false, err
		}

		latestVersion := strings.TrimPrefix(releaseInfo.TagName, "v")
		currentVersionFull, _, _ := GetBuildInfo()
		currentVersionBase := ExtractBaseVersion(currentVersionFull)
		needsUpdate := NeedsUpdate(currentVersionBase, latestVersion)

		return latestVersion, needsUpdate, nil
	}
	defer func() { CheckLatestVersionImpl = originalImpl }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_, _, err := CheckLatestVersionWithContext(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "context deadline exceeded") // Timeout esperado
}
