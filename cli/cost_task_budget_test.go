package cli

import (
	"testing"

	"github.com/diillson/chatcli/models"
)

func trackerSpending(t *testing.T, limitUSD float64, hardStop bool, completionTokens int, costUSD float64) *CostTracker {
	t.Helper()
	ct := NewCostTrackerAt(t.TempDir())
	ct.mu.Lock()
	ct.budgetLimitUSD = limitUSD
	ct.budgetHardStop = hardStop
	ct.totalCompletionTokens = int64(completionTokens)
	ct.totalCostUSD = costUSD
	ct.mu.Unlock()
	return ct
}

// The conversion is measured: remaining dollars over this session's own
// cost per generated token. A price table and an assumption about the next
// turn would hand the model a number that is confidently wrong.
func TestTaskBudgetComesFromMeasuredSpend(t *testing.T) {
	// $10 limit, $2 spent producing 100k tokens → $0.00002/token,
	// $8 left → 400k tokens of room.
	ct := trackerSpending(t, 10, true, 100_000, 2)
	tokens, ok := ct.RemainingTaskBudgetTokens()
	if !ok {
		t.Fatal("a session with a ceiling and a measured rate must produce a budget")
	}
	if tokens < 390_000 || tokens > 410_000 {
		t.Errorf("budget = %d, want about 400000", tokens)
	}
}

// Without the hard stop the budget is a notice, not a ceiling, and there
// is nothing for the model to pace against.
func TestNoBudgetWithoutAHardStop(t *testing.T) {
	ct := trackerSpending(t, 10, false, 100_000, 2)
	if _, ok := ct.RemainingTaskBudgetTokens(); ok {
		t.Error("a soft budget must not be sent as a task budget")
	}
}

func TestNoBudgetWithoutALimit(t *testing.T) {
	ct := trackerSpending(t, 0, true, 100_000, 2)
	if _, ok := ct.RemainingTaskBudgetTokens(); ok {
		t.Error("no configured limit means no ceiling to express")
	}
}

// A handful of tokens on a cache-cold first turn is not a rate.
func TestNoBudgetBeforeThereIsARate(t *testing.T) {
	ct := trackerSpending(t, 10, true, 100, 0.01)
	if _, ok := ct.RemainingTaskBudgetTokens(); ok {
		t.Error("too few samples must not be extrapolated from")
	}
}

// Below the provider's floor there is nothing meaningful to say, and the
// run is about to be stopped anyway.
func TestNoBudgetBelowTheProviderFloor(t *testing.T) {
	// $0.10 left at $0.00002/token → 5000 tokens, under the 20000 floor.
	ct := trackerSpending(t, 2.10, true, 100_000, 2)
	if _, ok := ct.RemainingTaskBudgetTokens(); ok {
		t.Error("a budget under the provider floor must not be sent")
	}
}

func TestExhaustedBudgetSendsNothing(t *testing.T) {
	ct := trackerSpending(t, 2, true, 100_000, 2)
	if _, ok := ct.RemainingTaskBudgetTokens(); ok {
		t.Error("an exhausted budget has no room to report")
	}
}

// The daily limit counts too, and the nearer of the two wins.
func TestTheNearerLimitWins(t *testing.T) {
	ct := trackerSpending(t, 100, true, 100_000, 2)
	ct.mu.Lock()
	ct.dailyLimitUSD = 3
	ct.dailySpentUSD = 2
	ct.mu.Unlock()
	tokens, ok := ct.RemainingTaskBudgetTokens()
	if !ok {
		t.Fatal("a daily ceiling must produce a budget")
	}
	// $1 left daily, not $98 session.
	if tokens > 60_000 {
		t.Errorf("budget = %d, want the daily remainder (about 50000)", tokens)
	}
}

func TestNilTrackerIsSafe(t *testing.T) {
	var ct *CostTracker
	if _, ok := ct.RemainingTaskBudgetTokens(); ok {
		t.Error("a session without cost tracking has no budget to report")
	}
	_ = models.UsageInfo{}
}
