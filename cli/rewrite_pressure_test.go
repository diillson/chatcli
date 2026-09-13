package cli

import (
	"testing"

	"go.uber.org/zap"
)

// A warm prefix cache is worth more than the few hundred tokens an
// age-based rewrite saves: while the history is well under the budget the
// passes wait.
func TestHistoryRewritesWaitWhileTheCacheIsWarm(t *testing.T) {
	cli := warmCLI(t)
	cfg := DefaultCompactConfig(cli.Provider, cli.Model)
	budget := cli.historyCompactor.CharBudget(cfg)
	if budget <= 0 {
		t.Fatalf("unexpected budget %d", budget)
	}
	if cli.historyRewriteAllowed(historyOf(budget/2), cfg) {
		t.Error("a half-full window with a warm cache should not be rewritten")
	}
}

// Once the history presses on the compaction budget, shrinking it is what
// keeps the far more expensive summarizing compaction away, so the passes
// run even against a warm cache.
func TestHistoryRewritesRunUnderWindowPressure(t *testing.T) {
	cli := warmCLI(t)
	cfg := DefaultCompactConfig(cli.Provider, cli.Model)
	budget := cli.historyCompactor.CharBudget(cfg)
	pressed := int(float64(budget)*rewritePressureRatio) + 1
	if !cli.historyRewriteAllowed(historyOf(pressed), cfg) {
		t.Error("a history at the pressure ratio must be rewritten")
	}
}

// With no cache observed there is no warm prefix to protect, so the
// passes keep their historical every-turn behavior.
func TestHistoryRewritesRunWithoutAnObservedCache(t *testing.T) {
	cli := &ChatCLI{
		logger:           zap.NewNop(),
		Provider:         "STACKSPOT",
		Model:            "stackspot-ai",
		costTracker:      NewCostTrackerAt(t.TempDir()),
		historyCompactor: NewHistoryCompactor(zap.NewNop()),
	}
	cfg := DefaultCompactConfig(cli.Provider, cli.Model)
	if !cli.historyRewriteAllowed(historyOf(10), cfg) {
		t.Error("a session that never reported cache tokens must rewrite as before")
	}
	var none *ChatCLI
	if !none.historyRewriteAllowed(historyOf(10), cfg) {
		t.Error("a nil receiver must not block the rewrites")
	}
}
