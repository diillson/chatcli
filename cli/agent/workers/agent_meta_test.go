package workers

import (
	"testing"
)

func TestBuiltinAgentMeta_Defaults(t *testing.T) {
	m := NewBuiltinAgentMeta("PLANNER", "opus-4-6", "high")
	if got := m.Model(); got != "opus-4-6" {
		t.Errorf("Model() = %q, want %q", got, "opus-4-6")
	}
	if got := m.Effort(); got != "high" {
		t.Errorf("Effort() = %q, want %q", got, "high")
	}
}

func TestBuiltinAgentMeta_EnvOverride(t *testing.T) {
	m := NewBuiltinAgentMeta("FORMATTER", "", "low")

	// Set env vars; t.Setenv handles cleanup.
	t.Setenv("CHATCLI_AGENT_FORMATTER_MODEL", "claude-haiku-4-5")
	t.Setenv("CHATCLI_AGENT_FORMATTER_EFFORT", "medium")

	if got := m.Model(); got != "claude-haiku-4-5" {
		t.Errorf("Model() env override failed: got %q", got)
	}
	if got := m.Effort(); got != "medium" {
		t.Errorf("Effort() env override failed: got %q", got)
	}
}

func TestBuiltinAgentMeta_EmptyAgentName(t *testing.T) {
	// Defensive: no name = no env lookup, just return defaults.
	m := BuiltinAgentMeta{DefaultModel: "gpt-5", DefaultEffort: "medium"}
	if got := m.Model(); got != "gpt-5" {
		t.Errorf("Model() empty-name = %q", got)
	}
	if got := m.Effort(); got != "medium" {
		t.Errorf("Effort() empty-name = %q", got)
	}
}

func TestBuiltinAgentMeta_EffortLowercased(t *testing.T) {
	t.Setenv("CHATCLI_AGENT_REVIEWER_EFFORT", "HIGH")
	m := NewBuiltinAgentMeta("REVIEWER", "", "high")
	if got := m.Effort(); got != "high" {
		t.Errorf("Effort() should be lowercased, got %q", got)
	}
}

func TestBuiltinAgentMeta_TrimsWhitespace(t *testing.T) {
	t.Setenv("CHATCLI_AGENT_CODER_MODEL", "  claude-sonnet-4-6  ")
	m := NewBuiltinAgentMeta("CODER", "", "medium")
	if got := m.Model(); got != "claude-sonnet-4-6" {
		t.Errorf("Model() should trim whitespace, got %q", got)
	}
}

// TestAll12BuiltinsHaveDefaults verifies that every built-in worker has a
// non-empty effort tier set. Guards against future refactors accidentally
// stripping the defaults from one of the 12 New*Agent constructors.
func TestAll12BuiltinsHaveDefaults(t *testing.T) {
	cases := []struct {
		name   string
		agent  WorkerAgent
		effort string // expected default effort
	}{
		{"file", NewFileAgent(), "low"},
		{"coder", NewCoderAgent(), "medium"},
		{"shell", NewShellAgent(), "low"},
		{"git", NewGitAgent(), "low"},
		{"search", NewSearchAgent(), "low"},
		{"planner", NewPlannerAgent(), "high"},
		{"reviewer", NewReviewerAgent(), "high"},
		{"tester", NewTesterAgent(), "medium"},
		{"refactor", NewRefactorAgent(), "high"},
		{"diagnostics", NewDiagnosticsAgent(), "high"},
		{"formatter", NewFormatterAgent(), "low"},
		{"deps", NewDepsAgent(), "low"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.agent.Effort(); got != tc.effort {
				t.Errorf("built-in %s: Effort() = %q, want %q", tc.name, got, tc.effort)
			}
			// Built-ins should not hard-code a model — user /switch should win.
			if got := tc.agent.Model(); got != "" {
				t.Errorf("built-in %s: Model() = %q, expected empty (inherit user)", tc.name, got)
			}
		})
	}
}

func TestCustomAgent_ReadsModelEffortFromPersona(t *testing.T) {
	// Build a minimal persona.Agent and verify CustomAgent picks up its
	// Model and Effort. The helper is tested in its own package, but we
	// keep a smoke test here to catch interface drift.
	// Avoid importing persona here to keep the test light — use a
	// direct CustomAgent literal instead.
	ca := &CustomAgent{
		agentType:  "my-reviewer",
		name:       "my-reviewer",
		modelHint:  "claude-opus-4-6",
		effortHint: "high",
	}
	if ca.Model() != "claude-opus-4-6" {
		t.Errorf("CustomAgent.Model() = %q", ca.Model())
	}
	if ca.Effort() != "high" {
		t.Errorf("CustomAgent.Effort() = %q", ca.Effort())
	}

	// Unset hints should return empty strings.
	blank := &CustomAgent{}
	if blank.Model() != "" || blank.Effort() != "" {
		t.Error("blank CustomAgent should have empty Model()/Effort()")
	}
}

// TestCatalogString_ShowsEffort verifies the orchestrator catalog now
// surfaces per-agent effort so the LLM can make informed routing decisions.
func TestCatalogString_ShowsEffort(t *testing.T) {
	r := NewRegistry()
	r.Register(NewPlannerAgent())   // effort=high
	r.Register(NewFormatterAgent()) // effort=low

	catalog := r.CatalogString()
	// Must mention both effort tiers so the orchestrator LLM can see the
	// difference between thorough and cheap workers.
	if !metaContains(catalog, "effort=high") {
		t.Errorf("catalog should advertise planner's effort=high. got:\n%s", catalog)
	}
	if !metaContains(catalog, "effort=low") {
		t.Errorf("catalog should advertise formatter's effort=low. got:\n%s", catalog)
	}
}

func metaContains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
