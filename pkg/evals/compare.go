/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package evals

import "sort"

// CaseDelta is one case whose verdict changed between two reports.
type CaseDelta struct {
	Key        string  `json:"key"`
	Before     string  `json:"before"`
	After      string  `json:"after"`
	ScoreDelta float64 `json:"score_delta"`
}

// Comparison is a current report measured against a baseline.
type Comparison struct {
	Regressions    []CaseDelta `json:"regressions"` // passed before, does not now
	Fixes          []CaseDelta `json:"fixes"`       // did not pass before, passes now
	Added          []string    `json:"added"`
	Removed        []string    `json:"removed"`
	Common         int         `json:"common"` // cases graded on both sides; the rates below cover only these
	PassRateBefore float64     `json:"pass_rate_before"`
	PassRateAfter  float64     `json:"pass_rate_after"`
	ScoreBefore    float64     `json:"score_before"`
	ScoreAfter     float64     `json:"score_after"`
	CostBefore     float64     `json:"cost_before"`
	CostAfter      float64     `json:"cost_after"`
}

// Compare measures current against baseline, case by case. Skipped cases on
// either side are neither regressions nor fixes: there is nothing to compare.
// Pass rate, score and cost are measured over the cases graded on BOTH sides,
// so a filtered run (--filter) compared with a full baseline compares like
// with like instead of reporting the missing cases as a drop.
func Compare(baseline, current *Report) *Comparison {
	cmp := &Comparison{}
	var common, passBefore, passAfter int
	before := map[string]*CaseResult{}
	for i := range baseline.Cases {
		before[baseline.Cases[i].Key()] = &baseline.Cases[i]
	}
	seen := map[string]bool{}
	for i := range current.Cases {
		cur := &current.Cases[i]
		key := cur.Key()
		seen[key] = true
		old, ok := before[key]
		if !ok {
			cmp.Added = append(cmp.Added, key)
			continue
		}
		if old.Status == StatusSkipped || cur.Status == StatusSkipped {
			continue
		}
		common++
		if old.Status == StatusPass {
			passBefore++
		}
		if cur.Status == StatusPass {
			passAfter++
		}
		cmp.ScoreBefore += old.MeanScore
		cmp.ScoreAfter += cur.MeanScore
		cmp.CostBefore += old.CostUSD + old.JudgeCostUSD
		cmp.CostAfter += cur.CostUSD + cur.JudgeCostUSD
		d := CaseDelta{Key: key, Before: old.Status, After: cur.Status, ScoreDelta: cur.MeanScore - old.MeanScore}
		switch {
		case old.Status == StatusPass && cur.Status != StatusPass:
			cmp.Regressions = append(cmp.Regressions, d)
		case old.Status != StatusPass && cur.Status == StatusPass:
			cmp.Fixes = append(cmp.Fixes, d)
		}
	}
	for key := range before {
		if !seen[key] {
			cmp.Removed = append(cmp.Removed, key)
		}
	}
	if common > 0 {
		n := float64(common)
		cmp.PassRateBefore = float64(passBefore) / n
		cmp.PassRateAfter = float64(passAfter) / n
		cmp.ScoreBefore /= n
		cmp.ScoreAfter /= n
	}
	cmp.Common = common
	sort.Strings(cmp.Added)
	sort.Strings(cmp.Removed)
	return cmp
}

// Regressed applies the CI gate: more case regressions than allowed, or a
// pass-rate drop beyond tolerance (both in [0,1] terms).
func (c *Comparison) Regressed(maxRegressions int, tolerance float64) bool {
	if len(c.Regressions) > maxRegressions {
		return true
	}
	return c.PassRateBefore-c.PassRateAfter > tolerance+1e-9
}
