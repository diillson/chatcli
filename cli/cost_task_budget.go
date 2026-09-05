/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

// Turning a spending ceiling into something the model can pace against.
//
// ChatCLI's budgets are money: a session limit, a daily limit, and a hard
// stop that refuses the next turn once either is exhausted. That works,
// and it is invisible to the model — a long run is cut off mid-task with
// no chance to wind down, and the partial work is lost.
//
// The provider's task budget is the other half: a token ceiling the model
// reads while generating, so it paces itself and finishes cleanly. The two
// are in different units, and inventing a conversion factor would hand the
// model a number that is confidently wrong.
//
// So the conversion is measured, not guessed. The session already records
// what it spent and how many completion tokens it produced; their ratio is
// this session's own cost per output token, on its own models, with its
// own cache behavior. Remaining dollars divided by that ratio is how many
// more tokens this session can afford to generate — derived from what
// actually happened rather than from a price table and an assumption
// about the shape of the next turn.
//
// Advisory by construction: the budget only ever tells the model roughly
// how much room is left. The hard stop remains the thing that enforces.

// taskBudgetMinTokens is the provider's floor for a task budget. Below it
// there is nothing meaningful to say, so nothing is sent — a run that
// close to its ceiling is about to be stopped anyway.
const taskBudgetMinTokens = 20000

// remainingBudgetUSD is what is left before the nearer of the two limits
// stops the run. Reports false when no limit is configured, or when the
// hard stop is not armed: without it the budget is a notice rather than a
// ceiling, and there is nothing for the model to pace against.
func (ct *CostTracker) remainingBudgetUSD() (float64, bool) {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	if !ct.budgetHardStop {
		return 0, false
	}
	remaining, ok := 0.0, false
	if ct.budgetLimitUSD > 0 {
		remaining, ok = ct.budgetLimitUSD-ct.totalCostUSD, true
	}
	if ct.dailyLimitUSD > 0 {
		daily := ct.dailyLimitUSD - ct.dailySpentUSD
		if !ok || daily < remaining {
			remaining, ok = daily, true
		}
	}
	if !ok || remaining <= 0 {
		return 0, false
	}
	return remaining, true
}

// costPerCompletionToken is what this session has actually paid, per token
// it generated. Reports false until the session has produced enough of
// both to make the ratio meaningful.
func (ct *CostTracker) costPerCompletionToken() (float64, bool) {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	if ct.totalCompletionTokens < taskBudgetMinSampleTokens || ct.totalCostUSD <= 0 {
		return 0, false
	}
	return ct.totalCostUSD / float64(ct.totalCompletionTokens), true
}

// costPerCompletionTokenOf is the same rate for one provider:model pair.
//
// The session-wide average is only a rate while the session stays on one
// model. A session that switched — an @model route, a fallback chain, a
// dedicated summarizer — blends prices that differ by more than an order
// of magnitude, and the ceiling the model reads is then denominated in a
// model that does not exist. Reports false until this pair has generated
// enough on its own, so the caller can fall back to the session average
// rather than extrapolate from a handful of tokens.
func (ct *CostTracker) costPerCompletionTokenOf(provider, model string) (float64, bool) {
	if provider == "" || model == "" {
		return 0, false
	}
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	rec, ok := ct.modelUsage[modelKey(provider, model)]
	if !ok || rec.CompletionTokens < taskBudgetMinSampleTokens || rec.TotalCostUSD <= 0 {
		return 0, false
	}
	return rec.TotalCostUSD / float64(rec.CompletionTokens), true
}

// taskBudgetMinSampleTokens is how much the session must have generated
// before its own cost ratio is worth extrapolating from. A handful of
// tokens on a cache-cold first turn is not a rate.
const taskBudgetMinSampleTokens = 2000

// RemainingTaskBudgetTokens converts the remaining spend into the token
// ceiling a task budget carries. Reports false when there is no ceiling to
// express, no measured rate to convert with, or so little room left that
// the provider's floor would reject it.
func (ct *CostTracker) RemainingTaskBudgetTokens() (int, bool) {
	return ct.RemainingTaskBudgetTokensFor("", "")
}

// RemainingTaskBudgetTokensFor is RemainingTaskBudgetTokens denominated in
// the tokens of the pair that actually serves the turn, falling back to the
// session average while that pair has no rate of its own. Callers that know
// their route should use this: the ceiling only means something in the
// currency the next turn will be billed in.
func (ct *CostTracker) RemainingTaskBudgetTokensFor(provider, model string) (int, bool) {
	if ct == nil {
		return 0, false
	}
	remainingUSD, ok := ct.remainingBudgetUSD()
	if !ok {
		return 0, false
	}
	perToken, ok := ct.costPerCompletionTokenOf(provider, model)
	if !ok {
		if perToken, ok = ct.costPerCompletionToken(); !ok {
			return 0, false
		}
	}
	tokens := int(remainingUSD / perToken)
	if tokens < taskBudgetMinTokens {
		return 0, false
	}
	return tokens, true
}
