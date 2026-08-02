package workers

import (
	"strings"
	"testing"
)

func TestFormatResultsIncludesRunID(t *testing.T) {
	r := AgentResult{CallID: "agent_1", Agent: AgentTypeCoder, Task: "implement X", Output: "done"}
	r.SetMetadata("run_id", "run-42")
	out := FormatResults([]AgentResult{r})
	if !strings.Contains(out, "Run: run-42") {
		t.Fatalf("expected run_id surfaced to the orchestrator, got:\n%s", out)
	}

	// Without metadata the line is omitted entirely.
	plain := FormatResults([]AgentResult{{CallID: "agent_2", Agent: AgentTypeFile, Task: "read"}})
	if strings.Contains(plain, "Run:") {
		t.Fatalf("unexpected Run line without metadata:\n%s", plain)
	}
}
