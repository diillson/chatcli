/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package depbot

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// DepsLabel is the Quality Gate bypass label for go.mod changes.
const DepsLabel = "deps:approved"

// PR is an open Dependabot pull request.
type PR struct {
	Number  int
	Title   string
	HeadSHA string
	Labels  []string
}

func (p PR) hasLabel(name string) bool {
	for _, l := range p.Labels {
		if l == name {
			return true
		}
	}
	return false
}

// Forge is the slice of GitHub the runner needs. The production
// implementation shells out to gh; tests use a fake.
type Forge interface {
	OpenPRs() ([]PR, error)
	Diff(pr PR) (string, error)
	Checks(pr PR) ([]Check, error)
	// BehindBy reports how many base-branch commits the head lacks.
	BehindBy(pr PR) (int, error)
	AddLabel(pr PR, label string) error
	UpdateBranch(pr PR) error
	// Merge must refuse when the head is no longer pr.HeadSHA.
	Merge(pr PR) error
	// Report publishes the verdict on the pull request. It is called on
	// every pass with the current text and must not notify when the text
	// has not changed.
	Report(pr PR, body string) error
}

// Action is what the runner does with one pull request.
type Action string

const (
	// ActionHold: the diff is not a plain bump; a human decides.
	ActionHold Action = "hold"
	// ActionLabel: apply deps:approved so Quality Gate Floor 4 accepts
	// the go.mod change the policy just verified.
	ActionLabel Action = "label"
	// ActionUpdate: bring the branch up to date with the base.
	ActionUpdate Action = "update"
	// ActionWait: checks are still running.
	ActionWait Action = "wait"
	// ActionFailed: a check failed on an up-to-date head.
	ActionFailed Action = "failed"
	// ActionMerge: everything passed.
	ActionMerge Action = "merge"
)

// Decide picks the action for one pull request. The order is the point:
//
//  1. An ineligible diff is held before anything is spent on it.
//  2. The label comes next because Floor 4 fails without it, so waiting
//     for checks first would only wait for a known failure.
//  3. A stale branch is updated before its checks are judged: the ruleset
//     requires an up-to-date head, and a result on a stale head — pass or
//     fail — says nothing about what would land on the base.
func Decide(policy Policy, checks ChecksResult, hasDepsLabel bool, behind int) Action {
	switch {
	case !policy.Eligible:
		return ActionHold
	case policy.NeedsDepsLabel && !hasDepsLabel:
		return ActionLabel
	case behind > 0:
		return ActionUpdate
	case checks.State == ChecksPending:
		return ActionWait
	case checks.State == ChecksFailed:
		return ActionFailed
	}
	return ActionMerge
}

// Outcome records what happened to one pull request in a pass.
type Outcome struct {
	PR     PR
	Action Action
	// Deferred is true when the action was postponed because an earlier
	// pull request holds the lane.
	Deferred bool
	Detail   []string
}

// Options configures a pass.
type Options struct {
	// Required lists checks that must be present and successful.
	Required []string
	// DryRun evaluates and logs without labeling, updating, merging or
	// reporting.
	DryRun bool
	Log    io.Writer
}

// Run makes one pass over the open Dependabot pull requests, oldest
// first.
//
// Merging is serialized through a single lane. Every merge leaves the
// other branches behind the base, and updating them all at once would
// re-run the whole CI for each of them after every merge. Instead the
// oldest pull request that is on its way to merging — just updated, or
// up to date and waiting for checks — holds the lane, and the ones behind
// it are only updated once it has merged or dropped out.
//
// A failure on one pull request does not stop the pass; the errors are
// joined so the job still ends red.
func Run(f Forge, opt Options) ([]Outcome, error) {
	if opt.Log == nil {
		opt.Log = io.Discard
	}
	prs, err := f.OpenPRs()
	if err != nil {
		return nil, fmt.Errorf("list pull requests: %w", err)
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].Number < prs[j].Number })

	var (
		outcomes []Outcome
		errs     []error
		laneHeld bool
	)
	for _, pr := range prs {
		out, err := evaluate(f, pr, opt)
		if err != nil {
			errs = append(errs, fmt.Errorf("#%d: %w", pr.Number, err))
			continue
		}
		if out.Action == ActionUpdate && laneHeld {
			out.Deferred = true
		}
		if err := apply(f, out, opt); err != nil {
			errs = append(errs, fmt.Errorf("#%d %s: %w", pr.Number, out.Action, err))
		}
		switch out.Action {
		case ActionUpdate, ActionWait, ActionLabel:
			laneHeld = true
		}
		fmt.Fprintf(opt.Log, "#%d %s%s — %s\n", pr.Number, out.Action, deferredSuffix(out), pr.Title)
		for _, d := range out.Detail {
			fmt.Fprintf(opt.Log, "    %s\n", d)
		}
		outcomes = append(outcomes, out)
	}
	return outcomes, errors.Join(errs...)
}

func deferredSuffix(o Outcome) string {
	if o.Deferred {
		return " (deferred: an earlier pull request is merging first)"
	}
	return ""
}

func evaluate(f Forge, pr PR, opt Options) (Outcome, error) {
	out := Outcome{PR: pr}
	diff, err := f.Diff(pr)
	if err != nil {
		return out, fmt.Errorf("diff: %w", err)
	}
	policy := EvaluateDiff(diff)
	if !policy.Eligible {
		// Skip the remaining API calls: nothing they return can make an
		// ineligible diff mergeable.
		out.Action, out.Detail = ActionHold, policy.Reasons
		return out, nil
	}
	checks, err := f.Checks(pr)
	if err != nil {
		return out, fmt.Errorf("checks: %w", err)
	}
	behind, err := f.BehindBy(pr)
	if err != nil {
		return out, fmt.Errorf("compare with base: %w", err)
	}
	res := EvaluateChecks(checks, opt.Required)
	out.Action = Decide(policy, res, pr.hasLabel(DepsLabel), behind)
	switch out.Action {
	case ActionWait:
		out.Detail = prefixed("waiting for: ", res.Waiting)
	case ActionFailed:
		out.Detail = prefixed("failed: ", res.Failing)
	case ActionUpdate:
		out.Detail = []string{fmt.Sprintf("%d commit(s) behind the base branch", behind)}
	}
	return out, nil
}

func prefixed(prefix string, items []string) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = prefix + it
	}
	return out
}

func apply(f Forge, out Outcome, opt Options) error {
	if opt.DryRun {
		return nil
	}
	// The report is refreshed first so a pull request that stops being
	// held or failing does not keep a stale explanation.
	if err := f.Report(out.PR, ReportBody(out)); err != nil {
		return fmt.Errorf("report: %w", err)
	}
	if out.Deferred {
		return nil
	}
	switch out.Action {
	case ActionLabel:
		return f.AddLabel(out.PR, DepsLabel)
	case ActionUpdate:
		return f.UpdateBranch(out.PR)
	case ActionMerge:
		return f.Merge(out.PR)
	}
	return nil
}

// ReportMarker identifies the runner's comment on a pull request.
const ReportMarker = "<!-- dependabot-automerge -->"

// ReportBody renders the comment for an outcome. Only the two states that
// need a human produce text; for every other state it returns "", which
// tells the forge to leave the pull request without a comment (or to
// retire the one a previous pass left).
func ReportBody(out Outcome) string {
	var headline string
	switch out.Action {
	case ActionHold:
		headline = "**Held for review.** This pull request is not a plain version bump, so it will not be merged automatically:"
	case ActionFailed:
		headline = "**Not merged: checks failed** on an up-to-date branch. Once they pass, the next run merges it:"
	default:
		return ""
	}
	var b strings.Builder
	b.WriteString(ReportMarker + "\n### Dependabot auto-merge\n\n" + headline + "\n\n")
	for _, d := range out.Detail {
		b.WriteString("- " + d + "\n")
	}
	return b.String()
}
