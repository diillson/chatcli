package workers

import (
	"strings"
	"testing"
)

func TestOrchestratorSystemPrompt(t *testing.T) {
	catalog := "### file (FileAgent)\nReads files\n"
	prompt := OrchestratorSystemPrompt(catalog)

	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(prompt, "MULTI-AGENT ORCHESTRATION MODE") {
		t.Error("expected 'MULTI-AGENT ORCHESTRATION MODE' in prompt")
	}
	if !strings.Contains(prompt, "agent_call") {
		t.Error("expected 'agent_call' dispatch syntax in prompt")
	}
	if !strings.Contains(prompt, "file") {
		t.Error("expected agent type 'file' in when-to-use section")
	}
	if !strings.Contains(prompt, "coder") {
		t.Error("expected agent type 'coder' in when-to-use section")
	}
	if !strings.Contains(prompt, "shell") {
		t.Error("expected agent type 'shell' in when-to-use section")
	}
	if !strings.Contains(prompt, "DECISION GUIDE") {
		t.Error("expected 'DECISION GUIDE' in prompt")
	}
	if !strings.Contains(prompt, "IMPORTANT RULES") {
		t.Error("expected 'IMPORTANT RULES' in prompt")
	}
	if !strings.Contains(prompt, catalog) {
		t.Error("expected catalog to be embedded in prompt")
	}
}

func TestOrchestratorSystemPrompt_EmptyCatalog(t *testing.T) {
	prompt := OrchestratorSystemPrompt("")
	if prompt == "" {
		t.Fatal("expected non-empty prompt even with empty catalog")
	}
	if !strings.Contains(prompt, "ORCHESTRATOR") {
		t.Error("expected ORCHESTRATOR in prompt")
	}
}

func TestOrchestratorSystemPrompt_WithFullCatalog(t *testing.T) {
	r := SetupDefaultRegistry()
	catalog := r.CatalogString()
	prompt := OrchestratorSystemPrompt(catalog)

	// Should contain all agent types from catalog
	for _, at := range []string{"file", "coder", "shell", "git", "search", "planner"} {
		if !strings.Contains(prompt, at) {
			t.Errorf("expected agent type %q in prompt with full catalog", at)
		}
	}
}
