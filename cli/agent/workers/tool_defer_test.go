package workers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

func mcpTool(name, desc string) models.ToolDefinition {
	return models.ToolDefinition{
		Type: "function",
		Function: models.ToolFunctionDef{
			Name:        name,
			Description: desc,
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"padding": map[string]interface{}{"type": "string", "description": strings.Repeat("x", 400)},
				},
			},
		},
	}
}

// A small tool set costs less than the round trip a search would add, so
// a plain install must be untouched.
func TestSmallToolSetIsNotDeferred(t *testing.T) {
	core := CoderToolDefinitions(nil)
	plan := PlanToolDefs(core, []models.ToolDefinition{mcpTool("jira_issue", "Fetch a Jira issue")}, nil, 1<<20)
	if plan.Deferred != 0 || plan.Index != "" {
		t.Fatalf("nothing should be deferred below the threshold: %+v", plan)
	}
	if len(plan.Defs) != len(core)+1 {
		t.Errorf("every definition must travel, got %d", len(plan.Defs))
	}
}

func TestLargeToolSetDefersAndAdvertises(t *testing.T) {
	core := CoderToolDefinitions(nil)
	var deferrable []models.ToolDefinition
	for _, n := range []string{"jira_issue", "grafana_query", "pagerduty_ack", "s3_list"} {
		deferrable = append(deferrable, mcpTool(n, "Does "+n+" things"))
	}
	plan := PlanToolDefs(core, deferrable, nil, 1000)
	if plan.Deferred != len(deferrable) {
		t.Fatalf("want every deferrable tool deferred, got %d", plan.Deferred)
	}
	// The core still travels, plus exactly one tool to recover the rest.
	names := map[string]bool{}
	for _, d := range plan.Defs {
		names[d.Function.Name] = true
	}
	if !names[FindToolsName] {
		t.Error("the search tool must travel or nothing is recoverable")
	}
	for _, d := range core {
		if !names[d.Function.Name] {
			t.Errorf("core tool %q must always travel", d.Function.Name)
		}
	}
	for _, d := range deferrable {
		if names[d.Function.Name] {
			t.Errorf("deferred tool %q must not be on the wire", d.Function.Name)
		}
		if !strings.Contains(plan.Index, d.Function.Name) {
			t.Errorf("deferred tool %q must be in the index", d.Function.Name)
		}
	}
	// The index has to be far cheaper than the schemas it replaces.
	full, _ := json.Marshal(deferrable)
	if len(plan.Index) >= len(full)/2 {
		t.Errorf("index (%d) should be much smaller than the schemas (%d)", len(plan.Index), len(full))
	}
}

func TestActivatedToolsTravelAgain(t *testing.T) {
	core := CoderToolDefinitions(nil)
	deferrable := []models.ToolDefinition{
		mcpTool("jira_issue", "Fetch a Jira issue"),
		mcpTool("s3_list", "List S3 objects"),
	}
	plan := PlanToolDefs(core, deferrable, map[string]bool{"jira_issue": true}, 1000)
	var found bool
	for _, d := range plan.Defs {
		if d.Function.Name == "jira_issue" {
			found = true
		}
	}
	if !found {
		t.Error("a tool the model already asked for must stay loaded")
	}
	if strings.Contains(plan.Index, "jira_issue") {
		t.Error("an activated tool must not still be advertised as deferred")
	}
}

func TestSearchToolDefsPrefersTheExactName(t *testing.T) {
	defs := []models.ToolDefinition{
		mcpTool("s3_list", "List objects in a bucket"),
		mcpTool("list_files", "List files in a directory, similar to listing objects"),
	}
	got := SearchToolDefs(defs, "s3_list", 5)
	if len(got) != 1 || got[0].Function.Name != "s3_list" {
		t.Fatalf("an exact name must win outright, got %+v", got)
	}
	got = SearchToolDefs(defs, "bucket objects", 5)
	if len(got) == 0 || got[0].Function.Name != "s3_list" {
		t.Errorf("description search failed: %+v", got)
	}
	if got := SearchToolDefs(defs, "  ", 5); got != nil {
		t.Errorf("an empty query returns nothing, got %+v", got)
	}
}

func TestDeferThresholdScalesWithTheWindow(t *testing.T) {
	small := DeferThresholdChars(0, 0)
	big := DeferThresholdChars(1000000, 4)
	if small != deferFloorChars {
		t.Errorf("unknown window must fall back to the floor, got %d", small)
	}
	if big <= small {
		t.Errorf("a large window must tolerate a larger tool set: %d vs %d", big, small)
	}
	if DeferThresholdChars(1000, 4) != deferFloorChars {
		t.Error("the floor must hold for a tiny window")
	}
}

func TestToolIndexIsStableAndReadable(t *testing.T) {
	defs := []models.ToolDefinition{
		mcpTool("b_tool", "Second. With more text after the first sentence."),
		mcpTool("a_tool", "First tool."),
	}
	idx := ToolIndex(defs)
	if strings.Index(idx, "a_tool") > strings.Index(idx, "b_tool") {
		t.Error("index must be sorted so the cached prefix is stable across reconnects")
	}
	if strings.Contains(idx, "With more text") {
		t.Error("index lines must stop at the first sentence")
	}
	if ToolIndex(nil) != "" {
		t.Error("no deferred tools means no index")
	}
}
