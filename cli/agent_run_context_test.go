package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func runContextAgent() *AgentMode {
	return &AgentMode{
		cli:                &ChatCLI{logger: zap.NewNop()},
		logger:             zap.NewNop(),
		injectedSkillNames: map[string]bool{"zeta": true, "alpha": true},
	}
}

// The run's workspace context is a flagged history message: appended once,
// never repeated while the last copy is still in history, so the cached
// prefix only ever grows.
func TestRunContextIsAppendedOnceAndFlagged(t *testing.T) {
	a := runContextAgent()
	a.appendRunContext("  ")
	if len(a.cli.history) != 0 {
		t.Fatal("an empty block must not be appended")
	}
	a.appendRunContext("memory: prefer table-driven tests")
	a.appendRunContext("memory: prefer table-driven tests")
	if len(a.cli.history) != 1 {
		t.Fatalf("an identical block must not be appended twice, got %d messages", len(a.cli.history))
	}
	m := a.cli.history[0]
	if m.Role != "user" || !m.IsRunContext() || !m.IsInjectedContext() || m.IsTurnContext() {
		t.Fatalf("unexpected shape: role=%q meta=%+v", m.Role, m.Meta)
	}
	if !strings.HasPrefix(m.Content, runContextHeader) {
		t.Fatalf("the block must announce itself: %q", m.Content)
	}
	a.appendRunContext("memory: something else")
	if len(a.cli.history) != 2 {
		t.Fatalf("a different block must be appended, got %d messages", len(a.cli.history))
	}
}

// The skills block takes the mid-loop injection shape, with the run's
// skill names sorted into Meta so curation and aging can find it.
func TestRunSkillsCarryNamesAndDedupAgainstHistory(t *testing.T) {
	a := runContextAgent()
	a.appendRunSkills("")
	if len(a.cli.history) != 0 {
		t.Fatal("an empty skills block must not be appended")
	}
	a.appendRunSkills("## Skill: alpha\nbody")
	a.appendRunSkills("## Skill: alpha\nbody")
	if len(a.cli.history) != 1 {
		t.Fatalf("an identical skills block must not be appended twice, got %d", len(a.cli.history))
	}
	m := a.cli.history[0]
	if m.Role != "user" || m.Meta == nil || m.Meta.SkillNames != "alpha,zeta" {
		t.Fatalf("unexpected skills message: role=%q meta=%+v", m.Role, m.Meta)
	}
	if m.IsInjectedContext() {
		t.Fatal("a skills message keeps the mid-loop injection shape, not the injected-context flags")
	}
	// Once aging collapsed the block, the same skills may travel again.
	a.cli.history[0].Meta.SkillCollapsed = true
	a.appendRunSkills("## Skill: alpha\nbody")
	if len(a.cli.history) != 2 {
		t.Fatalf("a collapsed block no longer counts as present, got %d", len(a.cli.history))
	}
}

// The system message of a run no longer carries the volatile blocks, so
// two runs with different queries send byte-identical system parts.
func TestRunVolatileBlocksStayOutOfTheSystemMessage(t *testing.T) {
	first := buildAgentSystemMessage("core", "tools", "stable", "", "", "orch", "", "")
	second := buildAgentSystemMessage("core", "tools", "stable", "", "", "orch", "", "")
	if first.Content != second.Content || len(first.SystemParts) != 4 {
		t.Fatalf("system message must be byte-stable across runs: %q vs %q", first.Content, second.Content)
	}
	for i, p := range first.SystemParts {
		if p.CacheControl == nil {
			t.Errorf("part %d must be cached", i)
		}
	}
	var none *AgentMode
	none.appendRunContext("x")
	none.appendRunSkills("x")
	if (models.Message{}).IsInjectedContext() {
		t.Error("a plain message is not injected context")
	}
}
