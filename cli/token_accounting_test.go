/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/models"
)

// countingClient is a TokenCounter stub: it answers a fixed count and
// records how many times it was asked.
type countingClient struct {
	tokens int
	calls  int
}

func (c *countingClient) GetModelName() string { return "stub" }
func (c *countingClient) SendPrompt(context.Context, string, []models.Message, int) (string, error) {
	return "", nil
}
func (c *countingClient) CountTokens(context.Context, string, []models.Message) (int, error) {
	c.calls++
	return c.tokens, nil
}

func TestCalibrateExact_FeedsTheCalibratorFromTheProviderCount(t *testing.T) {
	cli := newTenantTestCLI(t)
	cli.Provider, cli.Model = "TESTPROV", "exact-model"
	cli.history = []models.Message{{Role: "user", Content: strings.Repeat("a", 4000)}}
	cc := &countingClient{tokens: 2000} // 2 chars per token
	cli.Client = cc
	n, ok := cli.calibrateExact(context.Background())
	if !ok || n != 2000 || cc.calls != 1 {
		t.Fatalf("exact count: n=%d ok=%v calls=%d", n, ok, cc.calls)
	}
	ratio, samples := globalTokenCalibrator.CharsPerToken("TESTPROV", "exact-model")
	if samples != 1 || ratio < 1.99 || ratio > 2.01 {
		t.Fatalf("ratio must come from the exact count: %.2f (%d samples)", ratio, samples)
	}
	// Pacing: first turn counts, the next seven do not, the ninth does.
	cli.calibrationTurns = 0
	for i := 0; i < 9; i++ {
		cli.maybeCalibrateExact(context.Background())
	}
	if cc.calls != 3 {
		t.Fatalf("expected 2 paced counts on 9 turns (+1 direct), got %d calls", cc.calls)
	}
}

func TestObserveTokenCalibrationChars_UsesMeasuredChars(t *testing.T) {
	cli := newTenantTestCLI(t)
	cli.observeTokenCalibrationChars("TESTPROV", "chars-model", 3000, &models.UsageInfo{PromptTokens: 1000, IsReal: true})
	ratio, samples := globalTokenCalibrator.CharsPerToken("TESTPROV", "chars-model")
	if samples != 1 || ratio < 2.99 || ratio > 3.01 {
		t.Fatalf("ratio must use the measured chars: %.2f (%d)", ratio, samples)
	}
	cli.observeTokenCalibrationChars("TESTPROV", "chars-model", 0, &models.UsageInfo{PromptTokens: 1000, IsReal: true})
	if _, samples := globalTokenCalibrator.CharsPerToken("TESTPROV", "chars-model"); samples != 1 {
		t.Fatal("zero chars must be ignored")
	}
}

func TestSummarizerInputBudget_FollowsSummarizerWindow(t *testing.T) {
	t.Setenv("CHATCLI_CONTEXT_WINDOW", "") // catalog windows
	cfg := DefaultCompactConfig("CLAUDEAI", "claude-sonnet-5")
	session := summarizerInputBudget(cfg)
	if session <= summaryInputFloor {
		t.Fatalf("a 200K session window must yield a real budget, got %d", session)
	}
	cfg.SummarizerProvider, cfg.SummarizerModel = "OLLAMA", "tiny-model" // 8K fallback window
	if got := summarizerInputBudget(cfg); got != summaryInputFloor {
		t.Fatalf("a small summarizer window floors the budget, got %d", got)
	}
}

func TestRenderSegmentForSummary_RestoresStubsAndKeepsHeadTail(t *testing.T) {
	layer := compress.NewLayerFromEnv(t.TempDir())
	original := strings.Repeat("full tool output line\n", 40)
	key, ok := layer.Archive(original)
	if !ok {
		t.Fatal("archive must work on a fresh layer")
	}
	stub := "[tool result summary] " + compress.FormatMarker(key)
	long := strings.Repeat("0123456789", 400) // 4000 chars
	msgs := []models.Message{
		{Role: "tool", Content: stub},
		{Role: "assistant", Content: long, ToolCalls: []models.ToolCall{{Name: "bash"}}},
	}
	out := renderSegmentForSummary(layer, msgs, 4000) // allowance 2000 each
	if !strings.Contains(out, "full tool output line") {
		t.Fatal("a stub whose original fits its allowance must be restored")
	}
	if !strings.Contains(out, summaryCutMarker) || !strings.Contains(out, "[tool_calls: bash]") {
		t.Fatal("over-long content keeps head/tail and tool calls are named")
	}
	if strings.Contains(out, compress.FormatMarker(key)) {
		t.Fatal("restored content replaces the stub marker")
	}
	// Original larger than the allowance: the stub stands.
	small := renderSegmentForSummary(layer, msgs[:1], 200)
	if !strings.Contains(small, compress.FormatMarker(key)) {
		t.Fatal("when the original does not fit, the stub (with its marker) stays")
	}
	if got := renderSegmentForSummary(nil, msgs[:1], 4000); !strings.Contains(got, compress.FormatMarker(key)) {
		t.Fatal("nil layer must be safe")
	}
}

func TestContextStatusReport_ExactHistoryFromCounter(t *testing.T) {
	cli := newTenantTestCLI(t)
	cli.Provider, cli.Model = "TESTPROV", "status-model"
	cli.history = []models.Message{{Role: "user", Content: strings.Repeat("q", 800)}}
	cli.Client = &countingClient{tokens: 200}
	r := cli.buildContextStatusReport(context.Background())
	if r.ExactHistoryTokens != 200 {
		t.Fatalf("exact history must come from the provider count, got %d", r.ExactHistoryTokens)
	}
	if r.CalibrationSamples == 0 || r.CharsPerToken < 3.99 || r.CharsPerToken > 4.01 {
		t.Fatalf("the count must refresh the ratio: %.2f (%d samples)", r.CharsPerToken, r.CalibrationSamples)
	}
	cli.showContextStatus(context.Background()) // renders the exact line without panicking
}
