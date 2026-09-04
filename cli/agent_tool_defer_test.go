package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func deferAgent(t *testing.T) *AgentMode {
	t.Helper()
	return &AgentMode{
		logger: zap.NewNop(),
		cli: &ChatCLI{
			logger:   zap.NewNop(),
			Provider: "CLAUDEAI",
			Model:    "claude-opus-5",
		},
	}
}

func deferrable(name, desc string) models.ToolDefinition {
	return models.ToolDefinition{
		Type:     "function",
		Function: models.ToolFunctionDef{Name: name, Description: desc},
	}
}

func TestHandleFindToolsIgnoresOtherCalls(t *testing.T) {
	a := deferAgent(t)
	if _, handled := a.handleFindTools(models.ToolCall{Name: "read_file"}); handled {
		t.Error("a normal tool call must fall through to the usual dispatch")
	}
}

// A tool the model pulls stays loaded for the rest of the run: needing it
// once usually means needing it again.
func TestHandleFindToolsLoadsAndActivates(t *testing.T) {
	a := deferAgent(t)
	a.deferrableTools = []models.ToolDefinition{
		deferrable("jira_issue", "Fetch a Jira issue by key"),
		deferrable("s3_list", "List objects in an S3 bucket"),
	}
	result, handled := a.handleFindTools(models.ToolCall{
		Name:      workers.FindToolsName,
		Arguments: map[string]interface{}{"query": "jira_issue"},
	})
	if !handled {
		t.Fatal("the search call must be answered in-process")
	}
	if !strings.Contains(result, "jira_issue") {
		t.Errorf("result must name the loaded tool: %q", result)
	}
	if !strings.Contains(result, "description") && !strings.Contains(result, "Fetch a Jira issue") {
		t.Errorf("result must carry the definition, not just the name: %q", result)
	}
	if !a.activatedTools["jira_issue"] {
		t.Error("a pulled tool must stay activated for the run")
	}
	if a.activatedTools["s3_list"] {
		t.Error("only the matched tool is activated")
	}
}

// An empty result is a real answer: the index does not hold what the model
// asked for and it should stop looking rather than retry.
func TestHandleFindToolsAnswersOnNoMatch(t *testing.T) {
	a := deferAgent(t)
	a.deferrableTools = []models.ToolDefinition{deferrable("jira_issue", "Fetch a Jira issue")}
	result, handled := a.handleFindTools(models.ToolCall{
		Name:      workers.FindToolsName,
		Arguments: map[string]interface{}{"query": "quantum teleportation"},
	})
	if !handled {
		t.Fatal("the search call must still be answered")
	}
	if !strings.Contains(strings.ToLower(result), "no tool") {
		t.Errorf("the model must be told nothing matched: %q", result)
	}
	if len(a.activatedTools) != 0 {
		t.Errorf("a miss must activate nothing: %+v", a.activatedTools)
	}
}

func TestToolDeferThresholdNeverBelowTheFloor(t *testing.T) {
	a := deferAgent(t)
	got := a.toolDeferThreshold()
	if got < workers.DeferThresholdChars(0, 0) {
		t.Errorf("threshold %d fell below the floor", got)
	}
	// A model with a large window tolerates a larger tool set than the floor.
	if got <= 0 {
		t.Errorf("threshold must be positive, got %d", got)
	}
	// No CLI at all still answers with the floor rather than zero.
	bare := &AgentMode{logger: zap.NewNop()}
	if bare.toolDeferThreshold() <= 0 {
		t.Error("a bare agent must still get the floor")
	}
}

func TestPrepareDeferredToolsWithoutMCP(t *testing.T) {
	a := deferAgent(t)
	if idx := a.prepareDeferredTools(); idx != "" {
		t.Errorf("no MCP server means nothing to defer, got %q", idx)
	}
	if a.activatedTools == nil {
		t.Error("the activation set must be initialized for the run")
	}
	if a.deferrableTools != nil {
		t.Errorf("nothing deferrable, got %+v", a.deferrableTools)
	}
}
