/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package depbot

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var required = []string{"Lint, Test and Generate Coverage", "Quality Gate"}

func run(name, workflow, status, conclusion, startedAt string) Check {
	return Check{Name: name, Workflow: workflow, Status: status, Conclusion: conclusion, StartedAt: startedAt}
}

func green() []Check {
	return []Check{
		run("Lint, Test and Generate Coverage", "CI", "COMPLETED", "SUCCESS", "2026-11-23T09:00:00Z"),
		run("Quality Gate", "Quality Gate", "COMPLETED", "SUCCESS", "2026-11-23T09:00:00Z"),
		run("Floor 11 · CRD drift", "Quality Gate", "COMPLETED", "SKIPPED", "2026-11-23T09:00:00Z"),
		{Context: "quality-gate/coverage", State: "SUCCESS"},
	}
}

func TestEvaluateChecks(t *testing.T) {
	t.Run("all passed", func(t *testing.T) {
		assert.Equal(t, ChecksResult{State: ChecksGreen}, EvaluateChecks(green(), required))
	})
	t.Run("a non-required check failing still blocks", func(t *testing.T) {
		res := EvaluateChecks(append(green(), run("Trivy — Operator Image", "Security Scan", "COMPLETED", "FAILURE", "")), required)
		assert.Equal(t, ChecksFailed, res.State)
		assert.Equal(t, []string{"Trivy — Operator Image"}, res.Failing)
	})
	t.Run("running check", func(t *testing.T) {
		res := EvaluateChecks(append(green(), run("Go Vulnerability Check", "Security Scan", "IN_PROGRESS", "", "")), required)
		assert.Equal(t, ChecksPending, res.State)
		assert.Equal(t, []string{"Go Vulnerability Check"}, res.Waiting)
	})
	t.Run("pending wins over failed", func(t *testing.T) {
		res := EvaluateChecks(append(green(),
			run("A", "X", "COMPLETED", "FAILURE", ""),
			run("B", "X", "QUEUED", "", "")), required)
		assert.Equal(t, ChecksPending, res.State)
	})
	t.Run("required check not registered yet", func(t *testing.T) {
		res := EvaluateChecks(green()[:1], required)
		assert.Equal(t, ChecksPending, res.State)
		assert.Equal(t, []string{"Quality Gate"}, res.Waiting)
	})
	t.Run("no checks at all is not green", func(t *testing.T) {
		assert.Equal(t, ChecksPending, EvaluateChecks(nil, required).State)
	})
	t.Run("skipped required check validated nothing", func(t *testing.T) {
		checks := []Check{
			run("Lint, Test and Generate Coverage", "CI", "COMPLETED", "SKIPPED", ""),
			run("Quality Gate", "Quality Gate", "COMPLETED", "SUCCESS", ""),
		}
		res := EvaluateChecks(checks, required)
		assert.Equal(t, ChecksPending, res.State)
		assert.Equal(t, []string{"Lint, Test and Generate Coverage"}, res.Waiting)
	})
	t.Run("superseded run does not outvote its replacement", func(t *testing.T) {
		checks := append(green(),
			run("Quality Gate", "Quality Gate", "COMPLETED", "FAILURE", "2026-11-23T08:00:00Z"),
			run("Floor 4 · AI smells", "Quality Gate", "COMPLETED", "CANCELLED", "2026-11-23T08:00:00Z"),
			run("Floor 4 · AI smells", "Quality Gate", "COMPLETED", "SUCCESS", "2026-11-23T09:00:00Z"))
		assert.Equal(t, ChecksGreen, EvaluateChecks(checks, required).State)
	})
	t.Run("a newer failure replaces an older pass", func(t *testing.T) {
		checks := append(green(), run("Quality Gate", "Quality Gate", "COMPLETED", "FAILURE", "2026-11-23T10:00:00Z"))
		assert.Equal(t, ChecksFailed, EvaluateChecks(checks, required).State)
	})
	t.Run("commit status states", func(t *testing.T) {
		assert.Equal(t, ChecksFailed, EvaluateChecks(append(green(), Check{Context: "x", State: "ERROR"}), required).State)
		assert.Equal(t, ChecksPending, EvaluateChecks(append(green(), Check{Context: "x", State: "PENDING"}), required).State)
	})
}

func TestDecideOrder(t *testing.T) {
	ok := Policy{Eligible: true}
	needsLabel := Policy{Eligible: true, NeedsDepsLabel: true}
	passed := ChecksResult{State: ChecksGreen}
	failed := ChecksResult{State: ChecksFailed}
	pending := ChecksResult{State: ChecksPending}

	assert.Equal(t, ActionHold, Decide(Policy{}, passed, true, 0), "an ineligible diff is never merged, even all green")
	assert.Equal(t, ActionLabel, Decide(needsLabel, failed, false, 3), "label before anything else")
	assert.Equal(t, ActionUpdate, Decide(needsLabel, failed, true, 3), "a stale head is updated before its failure is believed")
	assert.Equal(t, ActionUpdate, Decide(ok, passed, false, 1), "a stale head is not merged even when green")
	assert.Equal(t, ActionWait, Decide(ok, pending, false, 0))
	assert.Equal(t, ActionFailed, Decide(ok, failed, false, 0))
	assert.Equal(t, ActionMerge, Decide(ok, passed, false, 0))
	assert.Equal(t, ActionMerge, Decide(needsLabel, passed, true, 0))
}

// fakeForge is an in-memory Forge. Merging one pull request leaves every
// other one a commit behind, as on a real repository.
type fakeForge struct {
	prs    []PR
	diffs  map[int]string
	checks map[int][]Check
	behind map[int]int
	fail   map[string]error

	calls   []string
	reports map[int]string
}

func (f *fakeForge) record(op string, pr PR) error {
	call := fmt.Sprintf("%s #%d", op, pr.Number)
	f.calls = append(f.calls, call)
	return f.fail[call]
}

func (f *fakeForge) OpenPRs() ([]PR, error) { return f.prs, f.fail["list"] }
func (f *fakeForge) Diff(pr PR) (string, error) {
	return f.diffs[pr.Number], f.fail[fmt.Sprintf("diff #%d", pr.Number)]
}
func (f *fakeForge) Checks(pr PR) ([]Check, error)  { return f.checks[pr.Number], nil }
func (f *fakeForge) BehindBy(pr PR) (int, error)    { return f.behind[pr.Number], nil }
func (f *fakeForge) AddLabel(pr PR, _ string) error { return f.record("label", pr) }
func (f *fakeForge) UpdateBranch(pr PR) error       { return f.record("update", pr) }

func (f *fakeForge) Merge(pr PR) error {
	if err := f.record("merge", pr); err != nil {
		return err
	}
	for _, other := range f.prs {
		if other.Number != pr.Number {
			f.behind[other.Number]++
		}
	}
	return nil
}

func (f *fakeForge) Report(pr PR, body string) error {
	if f.reports == nil {
		f.reports = map[int]string{}
	}
	f.reports[pr.Number] = body
	return nil
}

var (
	actionBump = fileDiff(".github/workflows/1-ci.yml",
		"-      - uses: actions/setup-go@"+shaA+" # v7.0.0",
		"+      - uses: actions/setup-go@"+shaB+" # v7.1.0")
	rootModBump = fileDiff("go.mod",
		"-\tgithub.com/google/uuid v1.6.0",
		"+\tgithub.com/google/uuid v1.6.1")
)

func newFake(prs ...PR) *fakeForge {
	f := &fakeForge{prs: prs, diffs: map[int]string{}, checks: map[int][]Check{}, behind: map[int]int{}, fail: map[string]error{}}
	for _, pr := range prs {
		f.diffs[pr.Number] = actionBump
		f.checks[pr.Number] = green()
	}
	return f
}

func actions(outs []Outcome) map[int]Action {
	m := map[int]Action{}
	for _, o := range outs {
		m[o.PR.Number] = o.Action
	}
	return m
}

func TestRunMergesOneAndUpdatesOnlyTheNext(t *testing.T) {
	// Listed out of order on purpose: the pass goes oldest first.
	f := newFake(PR{Number: 12}, PR{Number: 10}, PR{Number: 11})
	outs, err := Run(f, Options{Required: required})
	require.NoError(t, err)
	// #10 merges; that leaves #11 and #12 behind. Only #11 is updated —
	// updating #12 too would re-run its CI for a base that is about to
	// move again when #11 merges.
	assert.Equal(t, []string{"merge #10", "update #11"}, f.calls)
	assert.Equal(t, map[int]Action{10: ActionMerge, 11: ActionUpdate, 12: ActionUpdate}, actions(outs))
	assert.True(t, outs[2].Deferred)
	assert.False(t, outs[1].Deferred)
}

func TestRunLabelsVerifiedGoModBumpInsteadOfMerging(t *testing.T) {
	f := newFake(PR{Number: 1})
	f.diffs[1] = rootModBump
	_, err := Run(f, Options{Required: required})
	require.NoError(t, err)
	assert.Equal(t, []string{"label #1"}, f.calls)

	// With the label in place the same pull request merges.
	f = newFake(PR{Number: 1, Labels: []string{DepsLabel}})
	f.diffs[1] = rootModBump
	_, err = Run(f, Options{Required: required})
	require.NoError(t, err)
	assert.Equal(t, []string{"merge #1"}, f.calls)
}

func TestRunHoldsIneligibleDiffAndSaysWhy(t *testing.T) {
	f := newFake(PR{Number: 1}, PR{Number: 2})
	f.diffs[1] = rootModBump + fileDiff("main.go", "+// extra")
	outs, err := Run(f, Options{Required: required})
	require.NoError(t, err)
	// The held pull request neither merges nor blocks the next one.
	assert.Equal(t, []string{"merge #2"}, f.calls)
	assert.Equal(t, ActionHold, outs[0].Action)
	assert.Contains(t, f.reports[1], ReportMarker)
	assert.Contains(t, f.reports[1], "main.go: not a dependency manifest")
	assert.Empty(t, f.reports[2], "a merging pull request gets no comment")
}

func TestRunReportsFailedChecksAndMovesOn(t *testing.T) {
	f := newFake(PR{Number: 1}, PR{Number: 2})
	f.checks[1] = append(green(), run("Go Vulnerability Check", "Security Scan", "COMPLETED", "FAILURE", ""))
	outs, err := Run(f, Options{Required: required})
	require.NoError(t, err)
	assert.Equal(t, []string{"merge #2"}, f.calls)
	assert.Equal(t, ActionFailed, outs[0].Action)
	assert.Contains(t, f.reports[1], "failed: Go Vulnerability Check")
}

func TestRunWaitingPullRequestHoldsTheLane(t *testing.T) {
	f := newFake(PR{Number: 1}, PR{Number: 2})
	f.checks[1] = nil // workflows not registered yet
	f.behind[2] = 1
	outs, err := Run(f, Options{Required: required})
	require.NoError(t, err)
	assert.Empty(t, f.calls)
	assert.Equal(t, ActionWait, outs[0].Action)
	assert.True(t, outs[1].Deferred)
}

func TestRunDryRunChangesNothing(t *testing.T) {
	f := newFake(PR{Number: 1}, PR{Number: 2})
	f.diffs[2] = fileDiff("main.go", "+x")
	var log bytes.Buffer
	outs, err := Run(f, Options{Required: required, DryRun: true, Log: &log})
	require.NoError(t, err)
	assert.Empty(t, f.calls)
	assert.Empty(t, f.reports)
	assert.Equal(t, map[int]Action{1: ActionMerge, 2: ActionHold}, actions(outs))
	assert.Contains(t, log.String(), "#1 merge")
	assert.Contains(t, log.String(), "main.go: not a dependency manifest")
}

func TestRunKeepsGoingAfterAnErrorAndStillFails(t *testing.T) {
	f := newFake(PR{Number: 1}, PR{Number: 2}, PR{Number: 3})
	f.fail["diff #1"] = errors.New("boom")
	f.fail["merge #2"] = errors.New("head moved")
	_, err := Run(f, Options{Required: required})
	require.Error(t, err)
	assert.ErrorContains(t, err, "#1: diff: boom")
	assert.ErrorContains(t, err, "#2 merge: head moved")
	// #2 did not merge, so #3 is still up to date and merges.
	assert.Equal(t, []string{"merge #2", "merge #3"}, f.calls)

	_, err = Run(&fakeForge{fail: map[string]error{"list": errors.New("no network")}}, Options{})
	assert.ErrorContains(t, err, "list pull requests")
}

func TestReportBodyOnlySpeaksWhenAHumanIsNeeded(t *testing.T) {
	for _, a := range []Action{ActionMerge, ActionWait, ActionUpdate, ActionLabel} {
		assert.Empty(t, ReportBody(Outcome{Action: a}), string(a))
	}
	assert.Contains(t, ReportBody(Outcome{Action: ActionHold, Detail: []string{"why"}}), "- why")
	assert.Contains(t, ReportBody(Outcome{Action: ActionFailed, Detail: []string{"failed: X"}}), "- failed: X")
}
