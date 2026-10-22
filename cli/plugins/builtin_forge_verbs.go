/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * builtin_forge_verbs.go — the verbs that close the loop after a PR is
 * red or green: merge, review, checkout, close and undraft a PR; create
 * and close issues; rerun a workflow; and read CI logs by pattern or tail
 * instead of the whole failed output. Listings take a state, a search, an
 * author and a label. Mapped for gh and glab alike.
 */
package plugins

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// forgeStates are the listing states a model may ask for.
var forgeStates = map[string]bool{"open": true, "closed": true, "merged": true, "all": true}

// forgeListFilters appends the state/search/author/label filters of a
// listing to args, in the CLI's own spelling.
func forgeListFilters(gh bool, inv forgeInvocation, args []string) ([]string, error) {
	if st := strings.ToLower(strings.TrimSpace(inv.state)); st != "" {
		if !forgeStates[st] {
			return nil, fmt.Errorf("@forge %s: unknown state %q (open, closed, merged or all)", inv.cmd, inv.state)
		}
		if gh {
			args = append(args, "--state", st)
		} else {
			switch st {
			case "all":
				args = append(args, "--all")
			case "open":
				// glab lists open items by default.
			default:
				args = append(args, "--"+st)
			}
		}
	}
	if s := strings.TrimSpace(inv.search); s != "" {
		args = append(args, "--search", s)
	}
	if a := strings.TrimSpace(inv.author); a != "" {
		args = append(args, "--author", a)
	}
	for _, l := range forgeSplitList(inv.labels) {
		args = append(args, "--label", l)
	}
	return args, nil
}

// forgeSplitList splits a comma-separated label list.
func forgeSplitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// buildForgePRExtraArgs maps pr-merge, pr-review, pr-checkout, pr-close
// and pr-ready.
func buildForgePRExtraArgs(gh bool, inv forgeInvocation) ([]string, error) {
	if err := forgeNeedNumber(inv); err != nil {
		return nil, err
	}
	switch inv.cmd {
	case "pr-merge":
		method := strings.ToLower(strings.TrimSpace(inv.method))
		if method == "" {
			method = "squash"
		}
		switch method {
		case "squash", "merge", "rebase":
		default:
			return nil, fmt.Errorf("@forge pr-merge: unknown method %q (squash, merge or rebase)", inv.method)
		}
		if gh {
			args := []string{"pr", "merge", inv.number, "--" + method}
			if inv.deleteBranch {
				args = append(args, "--delete-branch")
			}
			return args, nil
		}
		args := []string{"mr", "merge", inv.number}
		if method != "merge" {
			args = append(args, "--"+method)
		}
		if inv.deleteBranch {
			args = append(args, "--remove-source-branch")
		}
		return args, nil
	case "pr-review":
		event := strings.ToLower(strings.TrimSpace(inv.event))
		switch event {
		case "approve", "request-changes", "comment":
		case "":
			return nil, errors.New(`@forge pr-review: missing event — approve, request-changes or comment (with body)`)
		default:
			return nil, fmt.Errorf("@forge pr-review: unknown event %q (approve, request-changes or comment)", inv.event)
		}
		if event != "approve" && strings.TrimSpace(inv.body) == "" {
			return nil, fmt.Errorf("@forge pr-review: %s needs a body", event)
		}
		if gh {
			args := []string{"pr", "review", inv.number, "--" + event}
			if strings.TrimSpace(inv.body) != "" {
				args = append(args, "--body", inv.body)
			}
			return args, nil
		}
		if event == "approve" {
			return []string{"mr", "approve", inv.number}, nil
		}
		return []string{"mr", "note", inv.number, "--message", inv.body}, nil
	case "pr-checkout":
		if gh {
			return []string{"pr", "checkout", inv.number}, nil
		}
		return []string{"mr", "checkout", inv.number}, nil
	case "pr-close":
		if gh {
			return []string{"pr", "close", inv.number}, nil
		}
		return []string{"mr", "close", inv.number}, nil
	case "pr-ready":
		if gh {
			return []string{"pr", "ready", inv.number}, nil
		}
		return []string{"mr", "update", inv.number, "--ready"}, nil
	}
	return nil, fmt.Errorf("@forge: unknown cmd %q", inv.cmd)
}

// buildForgeIssueExtraArgs maps issue-create and issue-close.
func buildForgeIssueExtraArgs(gh bool, inv forgeInvocation) ([]string, error) {
	switch inv.cmd {
	case "issue-create":
		if strings.TrimSpace(inv.title) == "" {
			return nil, errors.New("@forge issue-create: missing title")
		}
		var args []string
		if gh {
			args = []string{"issue", "create", "--title", inv.title, "--body", inv.body}
			for _, l := range forgeSplitList(inv.labels) {
				args = append(args, "--label", l)
			}
		} else {
			args = []string{"issue", "create", "--title", inv.title, "--description", inv.body}
			if ls := forgeSplitList(inv.labels); len(ls) > 0 {
				args = append(args, "--label", strings.Join(ls, ","))
			}
		}
		return args, nil
	case "issue-close":
		if err := forgeNeedNumber(inv); err != nil {
			return nil, err
		}
		return []string{"issue", "close", inv.number}, nil
	}
	return nil, fmt.Errorf("@forge: unknown cmd %q", inv.cmd)
}

// buildForgeCIExtraArgs maps ci-rerun.
func buildForgeCIExtraArgs(gh bool, inv forgeInvocation) ([]string, error) {
	if inv.cmd != "ci-rerun" {
		return nil, fmt.Errorf("@forge: unknown cmd %q", inv.cmd)
	}
	if strings.TrimSpace(inv.run) == "" {
		return nil, errors.New("@forge ci-rerun: missing run id (from ci-status)")
	}
	if gh {
		args := []string{"run", "rerun", inv.run}
		if inv.failed {
			args = append(args, "--failed")
		}
		return args, nil
	}
	return []string{"ci", "retry", inv.run}, nil
}

// forgeFilterOutput narrows a log to the lines matching grep (a Go regexp,
// case-insensitive) and/or the last tail lines, saying how many were left
// out, so a 10k-line CI log comes back as the part that matters.
func forgeFilterOutput(out, grep string, tail int) (string, error) {
	if strings.TrimSpace(grep) == "" && tail <= 0 {
		return out, nil
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	total := len(lines)
	if g := strings.TrimSpace(grep); g != "" {
		re, err := regexp.Compile("(?i)" + g)
		if err != nil {
			return "", fmt.Errorf("@forge: invalid grep %q: %w", grep, err)
		}
		kept := lines[:0:0]
		for _, l := range lines {
			if re.MatchString(l) {
				kept = append(kept, l)
			}
		}
		lines = kept
	}
	matched := len(lines)
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	if len(lines) == 0 {
		return fmt.Sprintf("(no line of %d matches %q)", total, grep), nil
	}
	note := fmt.Sprintf("\n[%d of %d lines shown", len(lines), total)
	if strings.TrimSpace(grep) != "" {
		note += fmt.Sprintf(", %d matching %q", matched, grep)
	}
	if tail > 0 && matched > tail {
		note += fmt.Sprintf(", last %d", tail)
	}
	return strings.Join(lines, "\n") + note + "]", nil
}
