package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// The provider counts the tool definitions it was sent, so a sample that
// measures only the history reports fewer chars against the same tokens —
// and the ratio it teaches makes the budget compact too early.
func TestCalibrationCharsIncludeToolDefinitions(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: strings.Repeat("s", 500)},
		{Role: "user", Content: strings.Repeat("u", 1500)},
	}
	a := &AgentMode{logger: zap.NewNop(), toolDefsChars: 12911}

	got := a.calibrationChars(history)
	want := promptCharsOf(history) + 12911
	if got != want {
		t.Fatalf("calibrationChars = %d, want %d", got, want)
	}
	if got <= promptCharsOf(history) {
		t.Error("the tool catalog must widen the sample, not be ignored")
	}
}

func TestCalibrationCharsWithoutToolDefinitions(t *testing.T) {
	history := []models.Message{{Role: "user", Content: "hi"}}
	a := &AgentMode{logger: zap.NewNop()}
	if got, want := a.calibrationChars(history), promptCharsOf(history); got != want {
		t.Errorf("a run with no tool catalog must measure the history alone: %d vs %d", got, want)
	}
}

// Only a real usage report teaches the ratio: an estimate would train it
// on the ratio it was derived from.
func TestObserveCalibrationIgnoresEstimates(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop(), stateRoot: t.TempDir()}
	a := &AgentMode{logger: zap.NewNop(), cli: cli, toolDefsChars: 4000}
	history := []models.Message{{Role: "user", Content: strings.Repeat("u", 4000)}}

	a.observeCalibration("CLAUDEAI", "claude-opus-5", history, nil)
	if _, samples := cli.calibrator().CharsPerToken("CLAUDEAI", "claude-opus-5"); samples != 0 {
		t.Fatalf("a nil usage must teach nothing, got %d samples", samples)
	}
	a.observeCalibration("CLAUDEAI", "claude-opus-5", history,
		&models.UsageInfo{PromptTokens: 2000, CompletionTokens: 10})
	if _, samples := cli.calibrator().CharsPerToken("CLAUDEAI", "claude-opus-5"); samples != 0 {
		t.Fatalf("an estimate must teach nothing, got %d samples", samples)
	}

	a.observeCalibration("CLAUDEAI", "claude-opus-5", history,
		&models.UsageInfo{IsReal: true, PromptTokens: 2000, CompletionTokens: 10})
	ratio, samples := cli.calibrator().CharsPerToken("CLAUDEAI", "claude-opus-5")
	if samples == 0 {
		t.Fatal("a real usage report must produce a sample")
	}
	// The sample carries the tool catalog, so the ratio is wider than the
	// history alone would have taught.
	historyOnly := float64(promptCharsOf(history)) / 2000.0
	if ratio <= historyOnly {
		t.Errorf("ratio %.2f should exceed the history-only ratio %.2f", ratio, historyOnly)
	}
}

func TestObserveCalibrationWithoutCLI(t *testing.T) {
	a := &AgentMode{logger: zap.NewNop()}
	a.observeCalibration("CLAUDEAI", "claude-opus-5", nil, &models.UsageInfo{IsReal: true, PromptTokens: 1})
}
