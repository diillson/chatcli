package version

import (
	"context"
	"errors"
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

	// Aponta o fetch DEFAULT (código real) para o servidor de teste.
	t.Setenv("CHATCLI_LATEST_VERSION_URL", server.URL)

	// Mock GetBuildInfoImpl para retornar versão comparável (não "dev")
	originalBuildImpl := GetBuildInfoImpl
	GetBuildInfoImpl = func() (string, string, string) {
		return "1.25.0", "abc1234", "2024-09-15"
	}
	defer func() { GetBuildInfoImpl = originalBuildImpl }()

	ctx := context.Background()
	latest, hasUpdate, err := CheckLatestVersionWithContext(ctx)

	assert.NoError(t, err)
	assert.Equal(t, "1.26.0", latest)
	assert.True(t, hasUpdate)
}

// TestGetReport_EnrichesFromRelease cobre o caso go install: sem ldflags o
// commit é "unknown" e a data vem aproximada do binário; quando a versão
// instalada é a própria release mais recente, o Report preenche ambos a
// partir dos metadados da release — sem mutar estado do pacote e sem exigir
// ordem de chamadas.
func TestGetReport_EnrichesFromRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tag_name": "v1.25.0", "target_commitish": "0123456789abcdef", "published_at": "2026-07-01T12:00:00Z"}`))
	}))
	defer server.Close()
	t.Setenv("CHATCLI_LATEST_VERSION_URL", server.URL)

	originalBuildImpl := GetBuildInfoImpl
	GetBuildInfoImpl = func() (string, string, string) {
		return "1.25.0", "unknown", "2026-07-10 09:00:00" + buildDateApproxSuffix
	}
	defer func() { GetBuildInfoImpl = originalBuildImpl }()

	rep := GetReport(context.Background())

	assert.NoError(t, rep.CheckErr)
	assert.Equal(t, "1.25.0", rep.Latest)
	assert.False(t, rep.NeedsUpdate)
	assert.Equal(t, "0123456789ab", rep.Current.CommitHash, "commit deve vir da release, truncado em 12")
	assert.Equal(t, "2026-07-01 12:00:00", rep.Current.BuildDate, "data aproximada deve ceder à data da release")

	// Pureza: as globais do pacote não podem ter sido tocadas.
	assert.Equal(t, "unknown", CommitHash)
	assert.Equal(t, "unknown", BuildDate)
}

// TestGetReport_NoEnrichmentOnVersionMismatch garante que metadados de uma
// release DIFERENTE da versão instalada nunca contaminam o build info.
func TestGetReport_NoEnrichmentOnVersionMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tag_name": "v1.26.0", "target_commitish": "0123456789abcdef", "published_at": "2026-07-01T12:00:00Z"}`))
	}))
	defer server.Close()
	t.Setenv("CHATCLI_LATEST_VERSION_URL", server.URL)

	originalBuildImpl := GetBuildInfoImpl
	GetBuildInfoImpl = func() (string, string, string) {
		return "1.25.0", "unknown", "unknown"
	}
	defer func() { GetBuildInfoImpl = originalBuildImpl }()

	rep := GetReport(context.Background())

	assert.True(t, rep.NeedsUpdate)
	assert.Equal(t, "unknown", rep.Current.CommitHash)
	assert.Equal(t, "unknown", rep.Current.BuildDate)
}

// TestGetReport_LdflagsWinOverRelease: um build de release (ldflags) já tem
// commit/data reais — os metadados do GitHub não devem sobrescrevê-los.
func TestGetReport_LdflagsWinOverRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tag_name": "v1.25.0", "target_commitish": "ffffffffffff", "published_at": "2026-07-01T12:00:00Z"}`))
	}))
	defer server.Close()
	t.Setenv("CHATCLI_LATEST_VERSION_URL", server.URL)

	originalBuildImpl := GetBuildInfoImpl
	GetBuildInfoImpl = func() (string, string, string) {
		return "1.25.0", "abc1234", "2026-06-30 08:00:00"
	}
	defer func() { GetBuildInfoImpl = originalBuildImpl }()

	rep := GetReport(context.Background())

	assert.Equal(t, "abc1234", rep.Current.CommitHash)
	assert.Equal(t, "2026-06-30 08:00:00", rep.Current.BuildDate)
}

func TestCheckLatestVersion_DisabledViaEnv(t *testing.T) {
	t.Setenv("CHATCLI_DISABLE_VERSION_CHECK", "true")

	ctx := context.Background()
	latest, needsUpdate, err := CheckLatestVersionWithContext(ctx)

	assert.NoError(t, err)
	assert.Empty(t, latest)
	assert.False(t, needsUpdate)

	// GetReport respeita o mesmo opt-out: só build info, sem rede.
	rep := GetReport(ctx)
	assert.NoError(t, rep.CheckErr)
	assert.Empty(t, rep.Latest)
	assert.False(t, rep.NeedsUpdate)
}

func TestCheckLatestVersion_DisabledCaseInsensitive(t *testing.T) {
	t.Setenv("CHATCLI_DISABLE_VERSION_CHECK", "TRUE")

	ctx := context.Background()
	latest, needsUpdate, err := CheckLatestVersionWithContext(ctx)

	assert.NoError(t, err)
	assert.Empty(t, latest)
	assert.False(t, needsUpdate)
}

func TestCheckLatestVersionWithContext_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // Simula delay
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tag_name": "v1.0.0"}`))
	}))
	defer server.Close()

	// Aponta o fetch DEFAULT (código real) para o servidor lento; o ctx do
	// caller expira antes da resposta.
	t.Setenv("CHATCLI_LATEST_VERSION_URL", server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_, _, err := CheckLatestVersionWithContext(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "context deadline exceeded") // Timeout esperado
}
