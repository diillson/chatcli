/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

func toolDef(name string) models.ToolDefinition {
	d := models.ToolDefinition{Type: "function"}
	d.Function.Name = name
	return d
}

// rebuildPendingNow reads the flag the telemetry consults on the next
// request, without waiting for one.
func rebuildPendingNow(ct *CostTracker) bool {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return ct.cache.rebuildPending
}

func clearRebuild(ct *CostTracker) {
	ct.mu.Lock()
	ct.cache.rebuildPending = false
	ct.mu.Unlock()
}

// TestNoteWireShape_DeclaresOurOwnRewrites covers the three request fields
// that live outside the history and decide prefix reuse. Each used to be
// counted against prefix stability: the alert after three unexplained
// misses fired at the user for decisions ChatCLI made.
func TestNoteWireShape_DeclaresOurOwnRewrites(t *testing.T) {
	cli := &ChatCLI{costTracker: NewCostTracker()}

	// The first turn establishes the shape: there is no cache to rebuild.
	cli.noteWireShape(client.EffortHigh, []models.ToolDefinition{toolDef("read_file")}, false)
	if rebuildPendingNow(cli.costTracker) {
		t.Fatal("the first turn of a session has no cache to declare a rebuild of")
	}
	// An unchanged shape is not a rebuild either, or the flag would be set
	// every turn and mean nothing.
	cli.noteWireShape(client.EffortHigh, []models.ToolDefinition{toolDef("read_file")}, false)
	if rebuildPendingNow(cli.costTracker) {
		t.Fatal("an unchanged request shape must not be reported as a rebuild")
	}

	for _, tc := range []struct {
		name   string
		effort client.SkillEffort
		defs   []models.ToolDefinition
		budget bool
	}{
		{"effort routed down", client.EffortLow, []models.ToolDefinition{toolDef("read_file")}, false},
		{"deferred tool activated", client.EffortLow, []models.ToolDefinition{toolDef("read_file"), toolDef("mcp_query")}, false},
		{"task budget appears", client.EffortLow, []models.ToolDefinition{toolDef("read_file"), toolDef("mcp_query")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearRebuild(cli.costTracker)
			cli.noteWireShape(tc.effort, tc.defs, tc.budget)
			if !rebuildPendingNow(cli.costTracker) {
				t.Fatalf("%s changes the prefix and must be declared as a rebuild", tc.name)
			}
		})
	}

	// Tool order is not identity: the same set serialized differently is
	// the same prefix and must not read as a rebuild.
	clearRebuild(cli.costTracker)
	cli.noteWireShape(client.EffortLow, []models.ToolDefinition{toolDef("mcp_query"), toolDef("read_file")}, true)
	if rebuildPendingNow(cli.costTracker) {
		t.Fatal("reordering the same tool set is not a prefix change")
	}
}

// mcpToolDef mimics what the manager hands the agent for one MCP tool.
func mcpToolDef(name, description string) models.ToolDefinition {
	d := toolDef("mcp_" + name)
	d.Function.Description = description
	return d
}

// TestFoldArrivedTools_ServerConnectingMidRun covers CMP-2: the deferred
// set is captured once, while the cached prompt is assembled, and MCP
// servers connect on their own schedule. A server that finished connecting
// after that was invisible twice over — its schemas did not travel, and it
// was not in the set find_tools searches.
func TestFoldArrivedTools_ServerConnectingMidRun(t *testing.T) {
	known := []models.ToolDefinition{mcpToolDef("query", "run a query")}
	activated := map[string]bool{}

	// A second server finishes connecting.
	current := append(append([]models.ToolDefinition{}, known...),
		mcpToolDef("deploy", "ship a release"),
		mcpToolDef("rollback", "undo a release"))

	arrived, activatedNow := foldArrivedTools(current, known, activated, 1<<20)
	if arrived != 2 {
		t.Fatalf("arrived = %d, want the two tools the new server brought", arrived)
	}
	if activatedNow != 2 || !activated["mcp_deploy"] || !activated["mcp_rollback"] {
		t.Fatalf("newcomers must travel so the model can learn they exist: %v", activated)
	}

	// Nothing new: no churn, and nothing is activated a second time.
	if arrived, activatedNow = foldArrivedTools(current, current, activated, 1<<20); arrived != 0 || activatedNow != 0 {
		t.Fatalf("an unchanged set must fold nothing, got arrived=%d activated=%d", arrived, activatedNow)
	}

	// A server disconnecting is a change too, and activates nothing.
	if arrived, _ = foldArrivedTools(known, current, map[string]bool{}, 1<<20); arrived != 0 {
		t.Fatalf("a shrinking set has no arrivals, got %d", arrived)
	}
}

// TestFoldArrivedTools_StaysInsideTheDeferBudget pins the bound: a large
// server connecting mid-run must not undo the deferral that made room in
// the first place. Past the threshold the newcomers stay searchable
// through find_tools rather than traveling.
func TestFoldArrivedTools_StaysInsideTheDeferBudget(t *testing.T) {
	big := strings.Repeat("a very long tool description. ", 40)
	known := []models.ToolDefinition{mcpToolDef("query", "run a query")}
	current := append(append([]models.ToolDefinition{}, known...),
		mcpToolDef("one", big), mcpToolDef("two", big), mcpToolDef("three", big))

	activated := map[string]bool{}
	threshold := toolDefChars(mcpToolDef("one", big)) * 2
	arrived, activatedNow := foldArrivedTools(current, known, activated, threshold)
	if arrived != 3 {
		t.Fatalf("arrived = %d, want 3", arrived)
	}
	if activatedNow != 2 {
		t.Fatalf("activation must stop at the defer threshold, activated %d of 3", activatedNow)
	}
	if activatedToolsChars(current, activated) > threshold {
		t.Fatal("the activated set outgrew the budget the run deferred under")
	}
}
