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
	if !strings.Contains(prompt, "MULTI-AGENT ORCHESTRATION") {
		t.Error("expected 'MULTI-AGENT ORCHESTRATION' in prompt")
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
	if !strings.Contains(prompt, "RULES") {
		t.Error("expected 'RULES' in prompt")
	}
	if !strings.Contains(prompt, catalog) {
		t.Error("expected catalog to be embedded in prompt")
	}
	if !strings.Contains(prompt, "SQUAD PLAYBOOK") {
		t.Error("expected 'SQUAD PLAYBOOK' section in prompt")
	}
	for _, tool := range []string{"@board", "@agents", "@mail", "@scheduler"} {
		if !strings.Contains(prompt, tool) {
			t.Errorf("expected squad tool %q in playbook", tool)
		}
	}
	if !strings.Contains(prompt, "[SQUAD MAIL]") {
		t.Error("expected the [SQUAD MAIL] inbox marker to be explained")
	}
}

func TestOrchestratorSystemPrompt_EmptyCatalog(t *testing.T) {
	prompt := OrchestratorSystemPrompt("")
	if prompt == "" {
		t.Fatal("expected non-empty prompt even with empty catalog")
	}
	if !strings.Contains(prompt, "ORCHESTRATION") {
		t.Error("expected ORCHESTRATION in prompt")
	}
}

func TestOrchestratorSystemPrompt_WithFullCatalog(t *testing.T) {
	r := SetupDefaultRegistry()
	catalog := r.CatalogString()
	prompt := OrchestratorSystemPrompt(catalog)

	// Should contain all agent types from catalog
	for _, at := range []string{"file", "coder", "shell", "git", "search", "planner", "reviewer", "tester", "refactor", "diagnostics", "formatter", "deps"} {
		if !strings.Contains(prompt, at) {
			t.Errorf("expected agent type %q in prompt with full catalog", at)
		}
	}
}
