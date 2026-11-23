/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package depbot

import (
	"sort"
	"strings"
)

// Check is one entry of a pull request's status check rollup, covering
// both shapes GitHub reports: check runs (Name/Status/Conclusion) and
// commit statuses (Context/State).
type Check struct {
	Name       string `json:"name"`
	Workflow   string `json:"workflowName"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Context    string `json:"context"`
	State      string `json:"state"`
	StartedAt  string `json:"startedAt"`
}

func (c Check) label() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Context
}

// ChecksState summarizes a rollup.
type ChecksState string

const (
	ChecksGreen   ChecksState = "green"
	ChecksPending ChecksState = "pending"
	ChecksFailed  ChecksState = "failed"
)

// ChecksResult is the verdict on a rollup. Failing and Waiting name the
// checks behind a non-green state.
type ChecksResult struct {
	State   ChecksState `json:"state"`
	Failing []string    `json:"failing,omitempty"`
	Waiting []string    `json:"waiting,omitempty"`
}

// outcome classifies one check: "ok", "pending" or "failed".
func (c Check) outcome() string {
	if c.Name == "" {
		switch strings.ToUpper(c.State) {
		case "SUCCESS":
			return "ok"
		case "PENDING", "EXPECTED", "":
			return "pending"
		}
		return "failed"
	}
	if strings.ToUpper(c.Status) != "COMPLETED" {
		return "pending"
	}
	switch strings.ToUpper(c.Conclusion) {
	case "SUCCESS", "SKIPPED", "NEUTRAL":
		return "ok"
	}
	return "failed"
}

// latestPerCheck keeps the most recent run of each check. A rollup lists
// every run on the head commit, so a run that was superseded — relabeled
// pull request, re-run, or one that concurrency cancelled — must not
// outvote the run that replaced it.
func latestPerCheck(checks []Check) []Check {
	latest := map[string]Check{}
	for _, c := range checks {
		key := c.Workflow + "\x00" + c.label()
		if prev, seen := latest[key]; !seen || c.StartedAt >= prev.StartedAt {
			latest[key] = c
		}
	}
	out := make([]Check, 0, len(latest))
	for _, c := range latest {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].label() < out[j].label() })
	return out
}

// EvaluateChecks reduces a rollup to one state. Every check on the commit
// must have passed — not only the ones the branch ruleset requires, since
// the vulnerability and image scans are exactly the checks that catch a
// bad bump. required lists checks that must be present and successful, so
// a commit whose workflows have not been registered yet reads as pending
// instead of vacuously green.
//
// Pending wins over failed: while anything is still running the verdict
// can change, and reporting a failure that a newer run is about to
// replace would be noise.
func EvaluateChecks(checks []Check, required []string) ChecksResult {
	var res ChecksResult
	passed := map[string]bool{}
	for _, c := range latestPerCheck(checks) {
		switch c.outcome() {
		case "pending":
			res.Waiting = append(res.Waiting, c.label())
		case "failed":
			res.Failing = append(res.Failing, c.label())
		default:
			// A skipped required check has validated nothing.
			if strings.ToUpper(c.Conclusion) != "SKIPPED" {
				passed[c.label()] = true
			}
		}
	}
	seen := map[string]bool{}
	for _, name := range append(append([]string{}, res.Waiting...), res.Failing...) {
		seen[name] = true
	}
	for _, name := range required {
		if !passed[name] && !seen[name] {
			res.Waiting = append(res.Waiting, name)
		}
	}
	switch {
	case len(res.Waiting) > 0:
		res.State = ChecksPending
	case len(res.Failing) > 0:
		res.State = ChecksFailed
	default:
		res.State = ChecksGreen
	}
	return res
}
