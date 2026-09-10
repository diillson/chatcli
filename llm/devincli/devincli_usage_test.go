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

	"github.com/diillson/chatcli/llm/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// sampleTrajectory mirrors the Devin CLI's ATIF export: a user step (never
// counted), agent steps with Devin-native metadata.metrics, one step with
// only the ATIF-standard metrics block, and a per-step ACU cost.
const sampleTrajectory = `{
  "schema_version": "ATIF-v1.4",
  "session_id": "s-1",
  "agent": {"name": "devin", "model_name": "claude-opus-5"},
  "steps": [
    {"step_id": 1, "source": "user", "message": "hi", "metadata": {"is_user_input": true, "metrics": {"input_tokens": 999999, "output_tokens": 999999}}},
    {"step_id": 2, "source": "agent", "model_name": "claude-opus-5-medium", "message": "...",
     "metadata": {"generation_model": "claude-opus-5-medium", "committed_acu_cost": 0.02,
                  "metrics": {"input_tokens": 12000, "output_tokens": 300, "cache_creation_tokens": 500, "cache_read_tokens": 11000}}},
    {"step_id": 3, "source": "agent", "message": "done",
     "metadata": {"committed_acu_cost": 0.01},
     "metrics": {"prompt_tokens": 1500.0, "completion_tokens": 200, "cached_tokens": 1000, "cost_usd": 0.0125}}
  ],
  "final_metrics": {"total_prompt_tokens": 13500, "total_completion_tokens": 500, "total_cost_usd": 0.0125}
}`

func TestParseTrajectoryUsage_SumsAgentSteps(t *testing.T) {
	got, err := parseTrajectoryUsage([]byte(sampleTrajectory))
	require.NoError(t, err)
	require.NotNil(t, got.Usage)
	assert.True(t, got.Usage.IsReal)
	assert.Equal(t, 13500, got.Usage.PromptTokens, "user steps never count")
	assert.Equal(t, 500, got.Usage.CompletionTokens)
	// Normalized: the Anthropic-backed step's cache write and read ARE
	// input (12000+500+11000), the ATIF-standard step's cached_tokens are
	// already inside its 1500 — 25000 input, 500 output.
	assert.Equal(t, 25000, got.Usage.InputTokensTotal)
	assert.Equal(t, 25500, got.Usage.TotalTokens)
	assert.Equal(t, 500, got.Usage.CacheCreationInputTokens)
	assert.Equal(t, 12000, got.Usage.CacheReadInputTokens, "ATIF cached_tokens folds into cache reads")
	assert.InDelta(t, 0.0125, got.Usage.CostUSD, 1e-9)
	assert.InDelta(t, 0.03, got.ACUCost, 1e-9)
	assert.Equal(t, "claude-opus-5-medium", got.Model)
}

func TestParseTrajectoryUsage_FallsBackToFinalMetrics(t *testing.T) {
	doc := `{"agent":{"model_name":"swe-1-7"},"steps":[{"source":"agent","message":"x"}],
	         "final_metrics":{"total_prompt_tokens":800,"total_completion_tokens":50,"total_cached_tokens":600,"total_cost_usd":0.001}}`
	got, err := parseTrajectoryUsage([]byte(doc))
	require.NoError(t, err)
	require.NotNil(t, got.Usage)
	assert.Equal(t, 800, got.Usage.PromptTokens)
	assert.Equal(t, 50, got.Usage.CompletionTokens)
	assert.Equal(t, 600, got.Usage.CacheReadInputTokens)
	assert.Equal(t, "swe-1-7", got.Model)
}

func TestParseTrajectoryUsage_NoMetricsMeansNil(t *testing.T) {
	got, err := parseTrajectoryUsage([]byte(`{"steps":[{"source":"agent","message":"x"}]}`))
	require.NoError(t, err)
	assert.Nil(t, got.Usage)

	_, err = parseTrajectoryUsage([]byte(`not json`))
	require.Error(t, err)
}

// exportingFake is a fake devin that writes the sample trajectory to the
// --export path and prints a framed reply; argv is recorded for inspection.
func exportingFake(t *testing.T, record string) string {
	t.Helper()
	fixture := filepath.Join(t.TempDir(), "trajectory.json")
	require.NoError(t, os.WriteFile(fixture, []byte(sampleTrajectory), 0o600))
	return fakeDevin(t, `
echo "$@" > `+record+`
prev=""
for a in "$@"; do
  if [ "$prev" = "--export" ]; then cp "`+fixture+`" "$a"; fi
  prev="$a"
done
printf '<<<CHATCLI_REPLY_BEGIN>>>\nok\n<<<CHATCLI_REPLY_END>>>\n'
`)
}

func TestSendPrompt_UsageComesFromTrajectoryExport(t *testing.T) {
	t.Cleanup(func() { exportUnsupported.Store(false) })
	record := filepath.Join(t.TempDir(), "argv")
	bin := exportingFake(t, record)

	c := NewClient(bin, "claude-opus-5", zap.NewNop(), 1, 0)
	var _ client.UsageAwareClient = c
	got, err := c.SendPrompt(context.Background(), "hi", nil, 0)
	require.NoError(t, err)
	assert.Equal(t, "ok", got)

	usage := c.LastUsage()
	require.NotNil(t, usage, "real usage must be read back from the export")
	assert.True(t, usage.IsReal)
	assert.Equal(t, 13500, usage.PromptTokens)
	assert.Equal(t, 500, usage.CompletionTokens)

	argv, err := os.ReadFile(record)
	require.NoError(t, err)
	assert.Contains(t, string(argv), "--export ")
	assert.Contains(t, string(argv), trajectoryFileName)
}

func TestSendPrompt_NoExportFileLeavesUsageNil(t *testing.T) {
	t.Cleanup(func() { exportUnsupported.Store(false) })
	bin := fakeDevin(t, `printf '<<<CHATCLI_REPLY_BEGIN>>>\nok\n<<<CHATCLI_REPLY_END>>>\n'`)
	c := NewClient(bin, "", zap.NewNop(), 1, 0)
	_, err := c.SendPrompt(context.Background(), "hi", nil, 0)
	require.NoError(t, err)
	assert.Nil(t, c.LastUsage(), "no export → nil → caller estimates, as before")
}

func TestSendPrompt_UsageNeverLeaksAcrossTurns(t *testing.T) {
	t.Cleanup(func() { exportUnsupported.Store(false) })
	record := filepath.Join(t.TempDir(), "argv")
	bin := exportingFake(t, record)
	c := NewClient(bin, "", zap.NewNop(), 1, 0)
	_, err := c.SendPrompt(context.Background(), "hi", nil, 0)
	require.NoError(t, err)
	require.NotNil(t, c.LastUsage())

	// Second turn against a binary that exports nothing: the previous
	// usage must not be re-counted.
	c.binPath = fakeDevin(t, `printf '<<<CHATCLI_REPLY_BEGIN>>>\nagain\n<<<CHATCLI_REPLY_END>>>\n'`)
	_, err = c.SendPrompt(context.Background(), "hi again", nil, 0)
	require.NoError(t, err)
	assert.Nil(t, c.LastUsage())
}

func TestSendPrompt_ExportDisabledByEnv(t *testing.T) {
	t.Cleanup(func() { exportUnsupported.Store(false) })
	t.Setenv("DEVIN_CLI_USAGE_EXPORT", "false")
	record := filepath.Join(t.TempDir(), "argv")
	bin := exportingFake(t, record)
	c := NewClient(bin, "", zap.NewNop(), 1, 0)
	_, err := c.SendPrompt(context.Background(), "hi", nil, 0)
	require.NoError(t, err)
	argv, err := os.ReadFile(record)
	require.NoError(t, err)
	assert.NotContains(t, string(argv), "--export")
	assert.Nil(t, c.LastUsage())
}

func TestSendPrompt_OldCLIRejectingExportIsRetriedWithoutIt(t *testing.T) {
	t.Cleanup(func() { exportUnsupported.Store(false) })
	record := filepath.Join(t.TempDir(), "argv")
	// Rejects --export like clap does; answers normally without it.
	bin := fakeDevin(t, `
echo "$@" >> `+record+`
case " $* " in
  *" --export "*) echo "error: unexpected argument '--export' found" >&2; exit 2;;
esac
printf '<<<CHATCLI_REPLY_BEGIN>>>\nok\n<<<CHATCLI_REPLY_END>>>\n'
`)
	c := NewClient(bin, "", zap.NewNop(), 1, 0)
	got, err := c.SendPrompt(context.Background(), "hi", nil, 0)
	require.NoError(t, err)
	assert.Equal(t, "ok", got)
	assert.True(t, exportUnsupported.Load(), "rejection must latch for the process")

	argv, err := os.ReadFile(record)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(argv)), "\n")
	require.Len(t, lines, 2, "one rejected attempt, one plain retry")
	assert.Contains(t, lines[0], "--export")
	assert.NotContains(t, lines[1], "--export")

	// Later turns skip the flag up front.
	_, err = c.SendPrompt(context.Background(), "hi again", nil, 0)
	require.NoError(t, err)
	argv, _ = os.ReadFile(record)
	lines = strings.Split(strings.TrimSpace(string(argv)), "\n")
	require.Len(t, lines, 3)
	assert.NotContains(t, lines[2], "--export")
}

func TestExportFlagRejected_IsNarrow(t *testing.T) {
	assert.True(t, exportFlagRejected("error: unexpected argument '--export' found"))
	assert.True(t, exportFlagRejected("Found argument '--export' which wasn't expected"))
	assert.False(t, exportFlagRejected("Error: Not logged in. Run `devin auth login`"))
	assert.False(t, exportFlagRejected("failed to write --export file: permission denied"))
}

// An enterprise Devin CLI reports ONLY the ATIF standard block and puts an
// Anthropic backend's cache write under metrics.extra. These are the
// numbers of a real turn (claude-sonnet-4.6, "reply with OK only"):
// prompt_tokens is the WHOLE input, cached_tokens and the extra write are
// subsets of it, and the write must land in the cache-creation pool or it
// is priced as plain input.
func TestParseTrajectoryUsage_StandardBlockExtraCacheWrite(t *testing.T) {
	const enterprise = `{"agent":{"model_name":"claude-sonnet-4.6"},"steps":[
	  {"source":"user","metadata":{"is_user_input":true}},
	  {"source":"agent","metrics":{"prompt_tokens":14274,"completion_tokens":4,"cached_tokens":9098,
	    "extra":{"cache_creation_input_tokens":5173}}}],
	  "final_metrics":{"total_prompt_tokens":14274,"total_completion_tokens":4,"total_cached_tokens":9098,"total_steps":2}}`
	got, err := parseTrajectoryUsage([]byte(enterprise))
	require.NoError(t, err)
	require.NotNil(t, got.Usage)
	assert.Equal(t, 14274, got.Usage.PromptTokens)
	assert.Equal(t, 4, got.Usage.CompletionTokens)
	assert.Equal(t, 9098, got.Usage.CacheReadInputTokens)
	assert.Equal(t, 5173, got.Usage.CacheCreationInputTokens, "extra.cache_creation_input_tokens is the cache write")
	assert.Equal(t, 14274, got.Usage.InputTokensTotal, "standard block: cache counts are a subset of prompt_tokens")
	assert.Equal(t, 14278, got.Usage.TotalTokens)
	assert.Zero(t, got.Usage.CostUSD, "no cost_usd anywhere in the document")
}

// A build that spells the read out under extra as well reports the same
// number twice; it must not be summed.
func TestParseTrajectoryUsage_ExtraReadIsNotDoubleCounted(t *testing.T) {
	const doc = `{"steps":[{"source":"agent","metrics":{"prompt_tokens":100,"completion_tokens":1,"cached_tokens":60,
	  "extra":{"cache_read_input_tokens":60}}}]}`
	got, err := parseTrajectoryUsage([]byte(doc))
	require.NoError(t, err)
	require.NotNil(t, got.Usage)
	assert.Equal(t, 60, got.Usage.CacheReadInputTokens)
}
