package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// loopHistory is three loop turns whose first tool result is large enough
// for microcompact to truncate once it is two turns old.
func loopHistory() []models.Message {
	big := strings.Repeat("old tool output line\n", 400)
	return []models.Message{
		{Role: "user", Content: "do the thing"},
		{Role: "assistant", Content: "", ToolCalls: []models.ToolCall{{ID: "c1"}}},
		{Role: "tool", ToolCallID: "c1", Content: big},
		{Role: "assistant", Content: "", ToolCalls: []models.ToolCall{{ID: "c2"}}},
		{Role: "tool", ToolCallID: "c2", Content: "ok"},
		{Role: "assistant", Content: "", ToolCalls: []models.ToolCall{{ID: "c3"}}},
		{Role: "tool", ToolCallID: "c3", Content: "ok"},
	}
}

// With no cache observed the passes keep their historical behavior: the
// old result is truncated and the coming cache write is declared.
func TestTurnBoundaryRewritesRunWithoutAnObservedCache(t *testing.T) {
	cli := &ChatCLI{
		logger:           zap.NewNop(),
		Provider:         "STACKSPOT",
		Model:            "stackspot-ai",
		costTracker:      NewCostTrackerAt(t.TempDir()),
		historyCompactor: NewHistoryCompactor(zap.NewNop()),
		history:          loopHistory(),
	}
	a := &AgentMode{cli: cli, logger: zap.NewNop(), skillCollapseTurn: map[string]int{}}
	before := len(cli.history[2].Content)

	a.applyTurnBoundaryRewrites(3, DefaultCompactConfig(cli.Provider, cli.Model), nil)

	if len(cli.history[2].Content) >= before {
		t.Fatalf("the two-turn-old result should have been truncated (%d -> %d)", before, len(cli.history[2].Content))
	}
	if !cli.costTracker.cache.rebuildPending {
		t.Error("a rewrite must declare the coming cache write as an expected rebuild")
	}
}

// Against a warm cache and a history far under the budget the same turn
// leaves every byte of the history alone.
func TestTurnBoundaryRewritesWaitWhileTheCacheIsWarm(t *testing.T) {
	cli := warmCLI(t)
	cli.history = loopHistory()
	a := &AgentMode{cli: cli, logger: zap.NewNop(), skillCollapseTurn: map[string]int{}}
	before := cli.history[2].Content

	a.applyTurnBoundaryRewrites(3, DefaultCompactConfig(cli.Provider, cli.Model), nil)

	if cli.history[2].Content != before {
		t.Fatal("a warm prefix must not be rewritten while the window is not under pressure")
	}
	if cli.costTracker.cache.rebuildPending {
		t.Error("nothing was rewritten, so no rebuild should be declared")
	}
	var none *AgentMode
	none.applyTurnBoundaryRewrites(0, CompactConfig{}, nil)
}
