package workers

import (
	"testing"
)

func TestParseAgentCalls_SingleSelfClosing(t *testing.T) {
	text := `<reasoning>Need to read files</reasoning>
<agent_call agent="file" task="read all .go files in pkg/coder/" />`

	calls, err := ParseAgentCalls(text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Agent != AgentTypeFile {
		t.Errorf("expected agent 'file', got '%s'", calls[0].Agent)
	}
	if calls[0].Task != "read all .go files in pkg/coder/" {
		t.Errorf("unexpected task: %s", calls[0].Task)
	}
	if calls[0].ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestParseAgentCalls_Multiple(t *testing.T) {
	text := `<reasoning>
1. Read files
2. Search for usages
</reasoning>
<agent_call agent="file" task="Read engine.go" />
<agent_call agent="search" task="Find all references to handleRead" />`

	calls, err := ParseAgentCalls(text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].Agent != AgentTypeFile {
		t.Errorf("expected agent 'file', got '%s'", calls[0].Agent)
	}
	if calls[1].Agent != AgentTypeSearch {
		t.Errorf("expected agent 'search', got '%s'", calls[1].Agent)
	}
}

func TestParseAgentCalls_PairedTag(t *testing.T) {
	text := `<agent_call agent="coder" task="Refactor the code">
Additional context here:
- rename X to Y
- update imports
</agent_call>`

	calls, err := ParseAgentCalls(text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Agent != AgentTypeCoder {
		t.Errorf("expected agent 'coder', got '%s'", calls[0].Agent)
	}
	// Task should include both the attribute and the body
	if calls[0].Task != "Refactor the code\nAdditional context here:\n- rename X to Y\n- update imports" {
		t.Errorf("unexpected task: %q", calls[0].Task)
	}
}

func TestParseAgentCalls_QuotedAttributes(t *testing.T) {
	text := `<agent_call agent="shell" task="Run go test ./... && echo 'done > output'" />`

	calls, err := ParseAgentCalls(text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Task != "Run go test ./... && echo 'done > output'" {
		t.Errorf("unexpected task: %q", calls[0].Task)
	}
}

func TestParseAgentCalls_SingleQuotes(t *testing.T) {
	text := `<agent_call agent='git' task='commit changes with message "fix bug"' />`

	calls, err := ParseAgentCalls(text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Agent != AgentTypeGit {
		t.Errorf("expected agent 'git', got '%s'", calls[0].Agent)
	}
}

func TestParseAgentCalls_MalformedSkipped(t *testing.T) {
	text := `<agent_call agent="file" />
Some text
<agent_call task="missing agent" />
<agent_call agent="coder" task="valid call" />`

	calls, err := ParseAgentCalls(text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// First has no task, second has no agent — both skipped
	if len(calls) != 1 {
		t.Fatalf("expected 1 valid call, got %d", len(calls))
	}
	if calls[0].Agent != AgentTypeCoder {
		t.Errorf("expected 'coder', got '%s'", calls[0].Agent)
	}
}

func TestParseAgentCalls_Empty(t *testing.T) {
	calls, err := ParseAgentCalls("No agent calls here, just regular text.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected 0 calls, got %d", len(calls))
	}
}

func TestParseAgentCalls_MixedWithToolCalls(t *testing.T) {
	text := `<reasoning>Plan</reasoning>
<agent_call agent="file" task="read files" />
<tool_call name="@coder" args='{"cmd":"read","args":{"file":"main.go"}}' />
<agent_call agent="shell" task="run tests" />`

	calls, err := ParseAgentCalls(text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should only parse agent_call tags, not tool_call
	if len(calls) != 2 {
		t.Fatalf("expected 2 agent_calls, got %d", len(calls))
	}
}

func TestParseAgentCalls_UniqueIDs(t *testing.T) {
	text := `<agent_call agent="file" task="task1" />
<agent_call agent="coder" task="task2" />
<agent_call agent="shell" task="task3" />`

	calls, err := ParseAgentCalls(text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ids := make(map[string]bool)
	for _, c := range calls {
		if ids[c.ID] {
			t.Fatalf("duplicate ID: %s", c.ID)
		}
		ids[c.ID] = true
	}
}
