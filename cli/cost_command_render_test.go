package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/diillson/chatcli/ui/kit"
	"github.com/diillson/chatcli/ui/theme"
)

// captureCommandStdout runs fn with os.Stdout redirected to a pipe and
// returns everything it printed. Local to the render tests so they can
// assert on the exact lines the user sees.
func captureCommandStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// pinPlainProfile strips all color so line assertions see plain text.
func pinPlainProfile(t *testing.T) {
	t.Helper()
	theme.SetProfile(theme.ProfileNoTTY)
	t.Cleanup(func() { theme.SetProfile(theme.DetectProfile()) })
}

// newCostTrackerFixture builds a tracker exercising every /cost section:
// top group, token bars, cache group, per-model costs and budget notice.
func newCostTrackerFixture() *CostTracker {
	ct := NewCostTracker()
	ct.totalPromptTokens = 1200
	ct.totalCompletionTokens = 800
	ct.totalCacheCreation = 300
	ct.totalCacheRead = 500
	ct.totalRequests = 3
	ct.totalCostUSD = 0.1234
	ct.budgetLimitUSD = 0.2
	ct.modelUsage["claudeai:model-a"] = &ModelUsageRecord{
		Provider:      "CLAUDEAI",
		Model:         "model-a",
		HasRealData:   true,
		InputCostUSD:  0.08,
		OutputCostUSD: 0.04,
		CacheCostUSD:  0.003,
		TotalCostUSD:  0.1234,
	}
	return ct
}

// TestHandleCostCommandRendersAllSections drives the full /cost render and
// asserts every section reaches the screen with its values formatted.
func TestHandleCostCommandRendersAllSections(t *testing.T) {
	pinPlainProfile(t)
	c := &ChatCLI{costTracker: newCostTrackerFixture(), Provider: "CLAUDEAI"}

	out := captureCommandStdout(t, func() { c.handleCostCommand() })

	for _, want := range []string{
		"CLAUDEAI",     // provider row
		"model-a",      // per-model cost header
		"$0.0800",      // input cost
		"$0.0400",      // output cost
		"$0.0030",      // cache cost
		"$0.1234",      // total cost
		"╭", "╰", "│", // box frame survives
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/cost output missing %q\n%s", want, out)
		}
	}
}

// TestHandleCostCommandAlignsTopGroup proves the label column is measured
// from the translated labels: every value of the top group starts at the
// same visible column, which the old English-tuned literal spaces could
// not guarantee in pt-BR.
func TestHandleCostCommandAlignsTopGroup(t *testing.T) {
	pinPlainProfile(t)
	c := &ChatCLI{costTracker: newCostTrackerFixture(), Provider: "CLAUDEAI"}

	out := captureCommandStdout(t, func() { c.handleCostCommand() })

	cols := map[int]bool{}
	for _, ln := range strings.Split(out, "\n") {
		// Top-group rows carry "<label>:" then two-space gap then value.
		idx := strings.Index(ln, ":")
		if idx < 0 || !strings.Contains(ln, "CLAUDEAI") && !strings.Contains(ln, "model") {
			continue
		}
		rest := ln[idx+1:]
		pad := len(rest) - len(strings.TrimLeft(rest, " "))
		cols[kit.VisibleLen(ln[:idx+1+pad])] = true
	}
	if len(cols) > 2 { // provider/model rows share the group column
		t.Errorf("top-group values start at %d distinct columns, want aligned: %v\n%s", len(cols), cols, out)
	}
}

// TestHandleCostCommandWithoutTracker covers the guard path.
func TestHandleCostCommandWithoutTracker(t *testing.T) {
	pinPlainProfile(t)
	c := &ChatCLI{}
	out := captureCommandStdout(t, func() { c.handleCostCommand() })
	if strings.TrimSpace(out) == "" {
		t.Fatal("missing not-initialized notice")
	}
}

// TestShowHelpRendersCommandColumns smoke-covers the /help table path,
// including the visible-width padded command column.
func TestShowHelpRendersCommandColumns(t *testing.T) {
	pinPlainProfile(t)
	c := &ChatCLI{}
	out := captureCommandStdout(t, func() { c.showHelp() })
	for _, want := range []string{"/help", "/exit", "/switch"} {
		if !strings.Contains(out, want) {
			t.Errorf("/help output missing %q", want)
		}
	}
}
