/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

// newLiveCostCLI builds a ChatCLI whose tracker carries real recorded usage
// (unlike the render fixture, whose fields are hand-set), so subcommands
// exercise the same paths production does.
func newLiveCostCLI(t *testing.T) *ChatCLI {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ct := NewCostTracker()
	ct.RecordRealUsage("OPENAI", "gpt-4o", &models.UsageInfo{
		PromptTokens: 10_000, CompletionTokens: 2_000, IsReal: true,
	})
	return &ChatCLI{costTracker: ct, Provider: "OPENAI"}
}

func TestCostCommandResetSubcommand(t *testing.T) {
	pinPlainProfile(t)
	c := newLiveCostCLI(t)
	oldID := c.costTracker.CurrentSessionID()

	out := captureCommandStdout(t, func() { c.handleCostCommand("reset") })

	if c.costTracker.TotalTokens() != 0 {
		t.Fatal("reset did not zero the live tracker")
	}
	if !strings.Contains(out, oldID) {
		t.Errorf("reset notice does not name the saved period id %q:\n%s", oldID, out)
	}
}

func TestCostCommandLastSubcommand(t *testing.T) {
	pinPlainProfile(t)
	c := newLiveCostCLI(t)

	// No previous snapshot yet (only the live session's own): "last" reports none.
	_ = c.costTracker.SaveSession()
	out := captureCommandStdout(t, func() { c.handleCostCommand("last") })
	if !strings.Contains(strings.ToLower(out), "no previous") && !strings.Contains(out, "anterior") {
		t.Errorf("expected 'none found' notice, got:\n%s", out)
	}

	// After a reset the closed period becomes the previous snapshot; both
	// the plain form and the --last spelling must find it.
	oldID := c.costTracker.CurrentSessionID()
	c.costTracker.Reset()
	for _, spelling := range []string{"last", "--last", "-l"} {
		out = captureCommandStdout(t, func() { c.handleCostCommand(spelling) })
		if !strings.Contains(out, oldID) {
			t.Errorf("/cost %s does not show the previous period %q:\n%s", spelling, oldID, out)
		}
	}
}

func TestCostCommandSessionsSubcommand(t *testing.T) {
	pinPlainProfile(t)
	c := newLiveCostCLI(t)
	_ = c.costTracker.SaveSession()

	out := captureCommandStdout(t, func() { c.handleCostCommand("sessions") })
	if !strings.Contains(out, c.costTracker.CurrentSessionID()) {
		t.Errorf("sessions listing missing the current snapshot:\n%s", out)
	}
}

func TestCostCommandExportSubcommand(t *testing.T) {
	pinPlainProfile(t)
	c := newLiveCostCLI(t)
	dest := filepath.Join(t.TempDir(), "cost.json")

	out := captureCommandStdout(t, func() { c.handleCostCommand("export " + dest) })
	if !strings.Contains(out, dest) {
		t.Errorf("export notice missing path:\n%s", out)
	}
	b, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("exported file: %v", err)
	}
	var snap SessionCostData
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatalf("exported JSON invalid: %v", err)
	}
	if snap.TotalRequests != 1 || snap.TotalTokens != 12_000 {
		t.Fatalf("exported snapshot wrong: %+v", snap)
	}
}

func TestCostCommandUnknownSubcommandShowsHelpThenSummary(t *testing.T) {
	pinPlainProfile(t)
	c := newLiveCostCLI(t)
	out := captureCommandStdout(t, func() { c.handleCostCommand("bogus") })
	if !strings.Contains(out, "/cost [reset") {
		t.Errorf("help line missing for unknown subcommand:\n%s", out)
	}
	if !strings.Contains(out, "gpt-4o") {
		t.Errorf("summary not rendered after help:\n%s", out)
	}
}

// TestCostCommandSummaryShowsPerModelSourceTags: a session mixing real and
// estimated models must tag each model line individually.
func TestCostCommandSummaryShowsPerModelSourceTags(t *testing.T) {
	pinPlainProfile(t)
	c := newLiveCostCLI(t)
	c.costTracker.RecordRealUsage("OLLAMA", "llama3.3", models.EstimateFromChars(4000, 400))

	out := captureCommandStdout(t, func() { c.handleCostCommand("") })
	if !strings.Contains(out, "(API)") {
		t.Errorf("per-model API tag missing:\n%s", out)
	}
}
