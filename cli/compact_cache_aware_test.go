package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// warmCLI returns a CLI whose cost tracker has just seen a cached request,
// so the prefix reads as warm.
func warmCLI(t *testing.T) *ChatCLI {
	t.Helper()
	cli := &ChatCLI{
		logger:           zap.NewNop(),
		Provider:         "CLAUDEAI",
		Model:            "claude-opus-5",
		costTracker:      NewCostTrackerAt(t.TempDir()),
		historyCompactor: NewHistoryCompactor(zap.NewNop()),
	}
	cli.costTracker.RecordRealUsage("CLAUDEAI", "claude-opus-5", &models.UsageInfo{
		IsReal:               true,
		PromptTokens:         10000,
		CompletionTokens:     100,
		CacheReadInputTokens: 9000,
	})
	return cli
}

func historyOf(chars int) []models.Message {
	return []models.Message{{Role: "user", Content: strings.Repeat("x", chars)}}
}

// A rewrite forces the provider to cache the whole prefix again, so an
// advisable pass waits while the cache it would throw away is still warm.
func TestCompactionDefersWhileTheCacheIsWarm(t *testing.T) {
	cli := warmCLI(t)
	cfg := DefaultCompactConfig(cli.Provider, cli.Model)
	budget := cli.historyCompactor.CharBudget(cfg)
	if budget <= 0 {
		t.Fatalf("unexpected budget %d", budget)
	}
	// Just over budget: worth doing eventually, not worth a cache rebuild now.
	if !cli.deferCompactionForWarmCache(historyOf(budget+budget/100), cfg) {
		t.Error("a modest overshoot with a warm cache should wait")
	}
}

// Overflowing the window is worse than any cache write, so past the hard
// ceiling the pass runs regardless.
func TestCompactionNeverDefersPastTheCeiling(t *testing.T) {
	cli := warmCLI(t)
	cfg := DefaultCompactConfig(cli.Provider, cli.Model)
	budget := cli.historyCompactor.CharBudget(cfg)
	if cli.deferCompactionForWarmCache(historyOf(budget*2), cfg) {
		t.Error("a history far past budget must compact even with a warm cache")
	}
}

func TestCompactionDoesNotDeferWithoutAWarmCache(t *testing.T) {
	cli := &ChatCLI{
		logger:           zap.NewNop(),
		Provider:         "CLAUDEAI",
		Model:            "claude-opus-5",
		costTracker:      NewCostTrackerAt(t.TempDir()),
		historyCompactor: NewHistoryCompactor(zap.NewNop()),
	}
	cfg := DefaultCompactConfig(cli.Provider, cli.Model)
	budget := cli.historyCompactor.CharBudget(cfg)
	if cli.deferCompactionForWarmCache(historyOf(budget+1), cfg) {
		t.Error("no cache activity means there is no warm prefix to protect")
	}
}

func TestCompactionDeferralIsSafeWithoutATracker(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop(), historyCompactor: NewHistoryCompactor(zap.NewNop())}
	if cli.deferCompactionForWarmCache(historyOf(100000), DefaultCompactConfig("CLAUDEAI", "claude-opus-5")) {
		t.Error("a session without cost tracking must never defer")
	}
	var nilCLI *ChatCLI
	if nilCLI.deferCompactionForWarmCache(nil, CompactConfig{}) {
		t.Error("nil receiver must not defer")
	}
}
