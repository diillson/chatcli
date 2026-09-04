package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func tc(text string) models.Message { return models.TurnContextMessage(text) }

func TestTurnContextIsRedundantOnlyOnAnExactRepeat(t *testing.T) {
	history := []models.Message{
		{Role: "user", Content: "hi"},
		tc("Current date: 2026-09-04"),
		{Role: "assistant", Content: "hello"},
	}
	if !turnContextIsRedundant(history, "Current date: 2026-09-04") {
		t.Error("an exact repeat must not be injected again")
	}
	// The day rolling over, a new working directory or a channel push all
	// change the text, and the block must travel again.
	if turnContextIsRedundant(history, "Current date: 2026-09-05") {
		t.Error("a changed block must travel")
	}
	if !turnContextIsRedundant(history, "   ") {
		t.Error("an empty block is nothing to say")
	}
	if turnContextIsRedundant(nil, "Current date: 2026-09-04") {
		t.Error("the first block of a session must always travel")
	}
}

func TestLastTurnContextTextReadsTheMostRecent(t *testing.T) {
	history := []models.Message{
		tc("first"),
		{Role: "user", Content: "x"},
		tc("second"),
		{Role: "assistant", Content: "y"},
	}
	if got := lastTurnContextText(history); got != "second" {
		t.Errorf("lastTurnContextText = %q, want the most recent", got)
	}
	if got := lastTurnContextText([]models.Message{{Role: "user", Content: "x"}}); got != "" {
		t.Errorf("no turn context must read as empty, got %q", got)
	}
}

func TestDropSupersededTurnContextKeepsTheNewest(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: "sys"},
		tc("date 1"),
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		tc("date 2"),
		{Role: "user", Content: "c"},
		tc("date 3"),
	}
	got := dropSupersededTurnContext(history)
	if len(got) != 5 {
		t.Fatalf("want 5 messages left, got %d: %+v", len(got), got)
	}
	var kept []string
	for _, m := range got {
		if m.IsTurnContext() {
			kept = append(kept, m.Content)
		}
	}
	if len(kept) != 1 || kept[0] != "date 3" {
		t.Errorf("only the newest block survives, kept %v", kept)
	}
	// Real conversation is untouched.
	for _, want := range []string{"sys", "a", "b", "c"} {
		found := false
		for _, m := range got {
			if m.Content == want {
				found = true
			}
		}
		if !found {
			t.Errorf("compaction dropped real content %q", want)
		}
	}
}

func TestDropSupersededTurnContextIsANoOpBelowTwo(t *testing.T) {
	history := []models.Message{{Role: "user", Content: "a"}, tc("only")}
	got := dropSupersededTurnContext(history)
	if len(got) != len(history) {
		t.Errorf("a single block must be left alone: %+v", got)
	}
	plain := []models.Message{{Role: "user", Content: "a"}}
	if got := dropSupersededTurnContext(plain); len(got) != 1 {
		t.Errorf("history without turn context must be untouched: %+v", got)
	}
}

// The session-invariant half of the old block belongs in the cached
// prefix; only the date is allowed to vary per turn.
func TestWorkspaceDirectiveLeavesTheVolatileBlock(t *testing.T) {
	dir := t.TempDir()
	cb := workspace.NewContextBuilder(
		workspace.NewBootstrapLoader(dir, dir, zap.NewNop()),
		workspace.NewMemoryStore(dir, zap.NewNop()),
		dir,
	)
	dyn := cb.BuildDynamicContext()
	if got := cb.BuildWorkspaceDirective(); !strings.Contains(got, "working directory") {
		t.Errorf("the directive must carry the working directory: %q", got)
	}
	if strings.Contains(dyn, "working directory") {
		t.Errorf("the working directory must not ride in the per-turn block: %q", dyn)
	}
	if !strings.Contains(dyn, "Current date") {
		t.Errorf("the per-turn block must still carry the date: %q", dyn)
	}
}
