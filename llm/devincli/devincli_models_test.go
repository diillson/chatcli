/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package devincli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/pricing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// sampleListing is a hand-reduced `devin models list --format json` that
// keeps every shape the parser must survive: a family whose slug differs
// from its uid, short aliases, the null-limit "adaptive" router, a legacy
// enum-style uid and a variant whose uid equals the family slug.
const sampleListing = `{
  "families": [
    {
      "family_label": "Claude Opus 5",
      "family_uid": "claude-opus-5",
      "slug": "claude-opus-5",
      "aliases": ["opus"],
      "variants": [
        {"model_uid": "claude-opus-5-medium", "label": "Claude Opus 5 Medium", "max_context_tokens": 1000000, "max_output_tokens": 128000, "cost_tier": "High cost", "cost_summary": "$5 / MTok In · $25 / MTok Out", "is_new": false, "is_beta": false},
        {"model_uid": "claude-opus-5-high-fast", "label": "Claude Opus 5 High Fast", "max_context_tokens": 1000000, "max_output_tokens": 128000, "is_new": false, "is_beta": false}
      ]
    },
    {
      "family_label": "Gemini 3.7 Flash",
      "family_uid": "gemini-3-7-flash",
      "slug": "gemini-3.7-flash",
      "aliases": ["gemini"],
      "variants": [
        {"model_uid": "gemini-3-7-flash-medium", "label": "Gemini 3.7 Flash Medium", "max_context_tokens": 1048576, "max_output_tokens": 65535}
      ]
    },
    {
      "family_label": "GLM-5.2",
      "family_uid": "glm-5.2",
      "slug": "glm-5.2",
      "aliases": [],
      "variants": [
        {"model_uid": "glm-5-2", "label": "GLM-5.2", "max_context_tokens": 200000, "max_output_tokens": 128000},
        {"model_uid": "glm-5-2-1m", "label": "GLM-5.2 1M", "max_context_tokens": 1000000, "max_output_tokens": 128000}
      ]
    },
    {
      "family_label": "GPT-5.2",
      "family_uid": "gpt-5.2",
      "slug": "gpt-5.2",
      "aliases": [],
      "variants": [
        {"model_uid": "MODEL_GPT_5_2_LOW", "label": "GPT-5.2 Low Thinking", "max_context_tokens": 384000, "max_output_tokens": 128000}
      ]
    },
    {
      "family_label": "Adaptive",
      "family_uid": "Adaptive",
      "slug": "adaptive",
      "aliases": [],
      "variants": [
        {"model_uid": "adaptive", "label": "Adaptive", "description": "Automatically balances quality and cost", "is_new": false, "is_beta": false}
      ]
    }
  ]
}`

// isolateSnapshot points the listing snapshot at a per-test file so tests
// never touch (or depend on) the user's ~/.chatcli state.
func isolateSnapshot(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "devin_models.json")
	prev := snapshotPath
	snapshotPath = func() (string, error) { return path, nil }
	// The pricing registry is process-global: start every listing test
	// from an empty DEVIN slate and leave none behind.
	pricing.ResetProvider(catalog.ProviderDevin)
	t.Cleanup(func() {
		snapshotPath = prev
		pricing.ResetProvider(catalog.ProviderDevin)
	})
	return path
}

func ids(models []client.ModelInfo) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}

func TestListModels_ProjectsFamiliesThenVariants(t *testing.T) {
	isolateSnapshot(t)
	fixture := filepath.Join(t.TempDir(), "listing.json")
	require.NoError(t, os.WriteFile(fixture, []byte(sampleListing), 0o600))
	record := filepath.Join(t.TempDir(), "argv")
	bin := fakeDevin(t, `echo "$@" > `+record+`; cat `+fixture)

	c := NewClient(bin, "", zap.NewNop(), 1, 0)
	models, err := c.ListModels(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{
		"claude-opus-5", "claude-opus-5-medium", "claude-opus-5-high-fast",
		"gemini-3.7-flash", "gemini-3-7-flash-medium",
		"glm-5.2", "glm-5-2", "glm-5-2-1m",
		"gpt-5.2",  // MODEL_GPT_5_2_LOW is skipped: not a --model id shape
		"adaptive", // the variant uid equals the slug → listed once
	}, ids(models))
	for _, m := range models {
		assert.Equal(t, client.ModelSourceAPI, m.Source, m.ID)
	}
	assert.Equal(t, "Claude Opus 5", models[0].DisplayName)
	assert.Equal(t, "Claude Opus 5 High Fast", models[2].DisplayName)

	argv, err := os.ReadFile(record)
	require.NoError(t, err)
	assert.Equal(t, "models list --format json", strings.TrimSpace(string(argv)),
		"listing must not carry the turn flags (-p, --model, --permission-mode)")
}

func TestListModels_RegistersDiscoveredSpecsInCatalog(t *testing.T) {
	isolateSnapshot(t)
	fixture := filepath.Join(t.TempDir(), "listing.json")
	require.NoError(t, os.WriteFile(fixture, []byte(sampleListing), 0o600))
	bin := fakeDevin(t, `cat `+fixture)

	_, err := NewClient(bin, "", zap.NewNop(), 1, 0).ListModels(context.Background())
	require.NoError(t, err)

	// A variant the static catalog never saw gets its own limits.
	assert.Equal(t, 1000000, catalog.GetContextWindow(catalog.ProviderDevin, "glm-5-2-1m"))
	assert.Equal(t, 200000, catalog.GetContextWindow(catalog.ProviderDevin, "glm-5-2"))
	// The family's limits follow its default (first) variant and override
	// the static entry: the backend enforces the CLI-reported values.
	assert.Equal(t, 200000, catalog.GetContextWindow(catalog.ProviderDevin, "glm-5.2"))
	meta, ok := catalog.Resolve(catalog.ProviderDevin, "glm-5.2")
	require.True(t, ok)
	assert.Equal(t, "glm-5.2", meta.ID)
	assert.Contains(t, meta.Capabilities, "tools")
	assert.Equal(t, catalog.APIChatCompletions, meta.PreferredAPI)

	// Short aliases and the hyphenated family uid resolve to the family.
	meta, ok = catalog.Resolve(catalog.ProviderDevin, "opus")
	require.True(t, ok)
	assert.Equal(t, "claude-opus-5", meta.ID)
	meta, ok = catalog.Resolve(catalog.ProviderDevin, "gemini-3-7-flash")
	require.True(t, ok)
	assert.Equal(t, "gemini-3.7-flash", meta.ID)
	assert.Equal(t, 65535, meta.MaxOutputTokens)

	// A variant id resolves to its own entry, never to the family's.
	meta, ok = catalog.Resolve(catalog.ProviderDevin, "claude-opus-5-high-fast")
	require.True(t, ok)
	assert.Equal(t, "claude-opus-5-high-fast", meta.ID)
	assert.Equal(t, "Claude Opus 5 High Fast", meta.DisplayName)

	// Null limits (adaptive) never overwrite with zero: the generic
	// fallback still applies rather than a 0-token window.
	assert.Greater(t, catalog.GetContextWindow(catalog.ProviderDevin, "adaptive"), 0)

	// Static aliases survive the upsert.
	meta, ok = catalog.Resolve(catalog.ProviderDevin, "claude-opus-5")
	require.True(t, ok)
	assert.Contains(t, meta.Aliases, "claude-opus-5")
	assert.Contains(t, meta.Aliases, "opus")
}

func TestListModels_RealCLIFixture(t *testing.T) {
	isolateSnapshot(t)
	// testdata/models_list.json is a verbatim `devin models list --format
	// json` capture (Sep 2026): 37 families / 168 variants, 19 of which
	// carry legacy enum-style uids and one ("adaptive") repeats its slug.
	bin := fakeDevin(t, `cat "`+filepath.Join(mustAbs(t, "testdata"), "models_list.json")+`"`)
	models, err := NewClient(bin, "", zap.NewNop(), 1, 0).ListModels(context.Background())
	require.NoError(t, err)
	assert.Len(t, models, 185)

	seen := map[string]bool{}
	for _, m := range models {
		assert.False(t, seen[m.ID], "duplicate id %s", m.ID)
		seen[m.ID] = true
		assert.Regexp(t, devinModelIDPattern, m.ID)
	}
	assert.True(t, seen["claude-opus-5"])
	assert.True(t, seen["gpt-5-6-sol-max-priority"])
	assert.True(t, seen["swe-1-6-fast"])
	assert.False(t, seen["MODEL_PRIVATE_11"])
	// The family that only has enum-style variants is still reachable.
	assert.True(t, seen["gpt-4.1"])
	assert.Equal(t, 400000, catalog.GetContextWindow(catalog.ProviderDevin, "gpt-5-3-codex-high"))
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	require.NoError(t, err)
	return abs
}

func TestParseDevinModels_ToleratesBannerAndFlatShapes(t *testing.T) {
	banner := "\x1b[33mA new version of devin is available\x1b[0m\n" + sampleListing
	families, err := parseDevinModels([]byte(banner))
	require.NoError(t, err)
	assert.Len(t, families, 5)

	flat := `{"models":[{"model_uid":"swe-1-6-fast","label":"SWE-1.6 Fast","max_context_tokens":200000,"max_output_tokens":128000}]}`
	families, err = parseDevinModels([]byte(flat))
	require.NoError(t, err)
	require.Len(t, families, 1)
	assert.Equal(t, "swe-1-6-fast", families[0].Slug)

	bare := `[{"model_uid":"kimi-k3-high","label":"Kimi K3 High"}]`
	families, err = parseDevinModels([]byte(bare))
	require.NoError(t, err)
	require.Len(t, families, 1)
	assert.Equal(t, "kimi-k3-high", families[0].Slug)

	models, _ := projectDevinModels(families, zap.NewNop())
	assert.Equal(t, []string{"kimi-k3-high"}, ids(models))

	_, err = parseDevinModels([]byte("Error: something unexpected"))
	require.Error(t, err)
	_, err = parseDevinModels([]byte(`{"families": [`))
	require.Error(t, err)
}

func TestListModels_AuthErrorIsActionable(t *testing.T) {
	bin := fakeDevin(t, `echo "Error: Not logged in. Run \`+"`"+`devin auth login\`+"`"+` to authenticate." >&2; exit 1`)
	_, err := NewClient(bin, "", zap.NewNop(), 1, 0).ListModels(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "devin auth login")
}

func TestListModels_NonJSONOutputIsDecodeError(t *testing.T) {
	bin := fakeDevin(t, `echo "Available models:"; echo "  claude-opus-5"`)
	_, err := NewClient(bin, "", zap.NewNop(), 1, 0).ListModels(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "devin models list --format json")
}

func TestListModels_ExecFailureCarriesStderr(t *testing.T) {
	bin := fakeDevin(t, `echo "boom: unexpected argument" >&2; exit 2`)
	_, err := NewClient(bin, "", zap.NewNop(), 1, 0).ListModels(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected argument")
}

func TestListModels_TimeoutKillsSubprocess(t *testing.T) {
	bin := fakeDevin(t, `sleep 5; echo '{"families":[]}'`)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := NewClient(bin, "", zap.NewNop(), 1, 0).ListModels(ctx)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second)
}

func TestListModels_EmptyListingIsNotAnError(t *testing.T) {
	isolateSnapshot(t)
	bin := fakeDevin(t, `echo '{"families":[]}'`)
	models, err := NewClient(bin, "", zap.NewNop(), 1, 0).ListModels(context.Background())
	require.NoError(t, err)
	assert.Empty(t, models)
}

func TestListModels_QueuedCallerHonorsContext(t *testing.T) {
	runMu.Lock()
	defer runMu.Unlock()
	bin := fakeDevin(t, `echo '{"families":[]}'`)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := NewClient(bin, "", zap.NewNop(), 1, 0).ListModels(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestClientImplementsModelLister(t *testing.T) {
	var _ client.ModelLister = (*Client)(nil)
}

func TestParseCostSummary(t *testing.T) {
	r, ok := parseCostSummary("$5 / MTok In · $25 / MTok Out")
	require.True(t, ok)
	assert.Equal(t, 5.0, r.InputPerMTok)
	assert.Equal(t, 25.0, r.OutputPerMTok)

	r, ok = parseCostSummary("$0.15 / MTok In · $0.5 / MTok Out")
	require.True(t, ok)
	assert.Equal(t, 0.15, r.InputPerMTok)
	assert.Equal(t, 0.5, r.OutputPerMTok)

	r, ok = parseCostSummary("$1.2/1M in, $6/1M out")
	require.True(t, ok)
	assert.Equal(t, 1.2, r.InputPerMTok)
	assert.Equal(t, 6.0, r.OutputPerMTok)

	_, ok = parseCostSummary("")
	assert.False(t, ok)
	_, ok = parseCostSummary("High cost")
	assert.False(t, ok)
}

func TestListModels_RegistersAccountRatesAndSnapshot(t *testing.T) {
	path := isolateSnapshot(t)
	t.Cleanup(func() { pricing.ResetProvider(catalog.ProviderDevin) })
	fixture := filepath.Join(t.TempDir(), "listing.json")
	require.NoError(t, os.WriteFile(fixture, []byte(sampleListing), 0o600))
	bin := fakeDevin(t, `cat `+fixture)

	_, err := NewClient(bin, "", zap.NewNop(), 1, 0).ListModels(context.Background())
	require.NoError(t, err)

	// Family rate = default (first) variant's cost_summary; a variant
	// without cost_summary stays unlisted (never "known free").
	r, ok := pricing.Lookup(catalog.ProviderDevin, "claude-opus-5")
	require.True(t, ok)
	assert.Equal(t, pricing.Rate{InputPerMTok: 5, OutputPerMTok: 25}, r)
	r, ok = pricing.Lookup(catalog.ProviderDevin, "claude-opus-5-medium")
	require.True(t, ok)
	assert.Equal(t, 25.0, r.OutputPerMTok)
	_, ok = pricing.Lookup(catalog.ProviderDevin, "claude-opus-5-high-fast")
	assert.False(t, ok)
	_, ok = pricing.Lookup(catalog.ProviderDevin, "adaptive")
	assert.False(t, ok)

	// Snapshot persisted with rates and specs, owner-only.
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"input_usd_per_mtok": 5`)
	assert.Contains(t, string(data), `"id": "glm-5-2-1m"`)

	// A fresh process (empty registries) restores everything from disk.
	pricing.ResetProvider(catalog.ProviderDevin)
	require.NoError(t, restoreSnapshot())
	r, ok = pricing.Lookup(catalog.ProviderDevin, "claude-opus-5")
	require.True(t, ok)
	assert.Equal(t, 5.0, r.InputPerMTok)
	assert.Equal(t, 1000000, catalog.GetContextWindow(catalog.ProviderDevin, "glm-5-2-1m"))
	meta, ok := catalog.Resolve(catalog.ProviderDevin, "opus")
	require.True(t, ok)
	assert.Equal(t, "claude-opus-5", meta.ID)
}

func TestRestoreSnapshot_MissingFileIsNotAnError(t *testing.T) {
	isolateSnapshot(t)
	require.NoError(t, restoreSnapshot())
}

func TestRestoreSnapshot_CorruptFileIsAnError(t *testing.T) {
	path := isolateSnapshot(t)
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))
	require.Error(t, restoreSnapshot())
}
