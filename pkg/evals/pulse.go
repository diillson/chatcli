/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * `chatcli eval` on the live dashboard. Each trial of each case is one
 * "eval:case" node, and the judge calls that grade it hang from that node.
 * The node carries how the trial went — mode, outcome, score, checks,
 * spend — and never the case's prompt, id, answer or error text: a suite
 * can hold private prompts, and node names stay one stable hub.
 */
package evals

import (
	"context"
	"strconv"

	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseCase is the dashboard node name of one trial.
const pulseCase = "eval:case"

// runTrialTraced runs one trial as a node on the live dashboard.
func runTrialTraced(ctx context.Context, j job, opts Options) TrialResult {
	ctx, span := pulse.BeginCtx(ctx, pulse.KindBackground, pulseCase, "")
	span.With("mode", caseMode(j.c)).With("trial", strconv.Itoa(j.n))
	tr := runTrial(ctx, j, opts)
	endCaseSpan(span, tr)
	return tr
}

// pulseSkippedTrial reports a trial that never ran (cancelled run, budget
// reached).
func pulseSkippedTrial(ctx context.Context, j job, reason string) {
	if !pulse.Enabled() {
		return
	}
	pulse.Point(pulse.KindBackground, pulseCase, pulse.ParentFrom(ctx), pulse.StatusCancelled, map[string]string{
		"mode":   caseMode(j.c),
		"trial":  strconv.Itoa(j.n),
		"state":  StatusSkipped,
		"reason": reason,
	})
}

// endCaseSpan closes a trial's node with its outcome.
func endCaseSpan(span *pulse.Span, tr TrialResult) {
	if span == nil {
		return
	}
	passed := 0
	for _, c := range tr.Checks {
		if c.Passed {
			passed++
		}
	}
	span.With("score", strconv.FormatFloat(tr.Score, 'f', 2, 64)).
		With("passed", strconv.FormatBool(tr.Status == StatusPass)).
		With("checks", strconv.Itoa(len(tr.Checks))).
		With("checks_passed", strconv.Itoa(passed)).
		With("provider", tr.Provider).
		With("model", tr.Model)
	if tr.Turns > 0 {
		span.With("turns", strconv.Itoa(tr.Turns))
	}
	if cost := tr.CostUSD + tr.JudgeCostUSD; cost > 0 {
		span.With("cost_usd", strconv.FormatFloat(cost, 'f', 4, 64))
	}
	status := pulse.StatusOK
	switch tr.Status {
	case StatusError:
		status = pulse.StatusError
	case StatusSkipped:
		status = pulse.StatusCancelled
	}
	span.Outcome(status, tr.Status)
}

// caseMode is the case's mode from the fixed set the loader accepts.
func caseMode(c *Case) string {
	if c == nil {
		return ""
	}
	switch c.Mode {
	case ModeChat, ModeCoder:
		return c.Mode
	}
	return ""
}
