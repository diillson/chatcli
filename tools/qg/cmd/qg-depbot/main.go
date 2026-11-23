/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// qg-depbot validates and merges Dependabot pull requests.
//
// One invocation makes one pass over the open Dependabot pull requests of
// the repository gh is pointed at (GH_REPO or the current checkout) and,
// for each, holds it, labels it, updates its branch, waits, or merges it.
// The rules live in tools/qg/depbot; this file is only the gh plumbing.
//
// Usage:
//
//	qg-depbot [-base main] [-dry-run] [-policy-only < diff]
//
// Required checks come from DEPBOT_REQUIRED_CHECKS, one name per line
// (names may contain commas, so a flag list would be ambiguous).
//
// -policy-only reads a unified diff on stdin and prints the policy verdict
// as JSON without touching GitHub; exit status 1 means "not eligible".
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/tools/qg/depbot"
)

func main() {
	base := flag.String("base", "main", "base branch")
	dryRun := flag.Bool("dry-run", false, "evaluate and log, change nothing")
	policyOnly := flag.Bool("policy-only", false, "judge a diff from stdin and exit")
	flag.Parse()

	if *policyOnly {
		os.Exit(policyFromStdin())
	}

	_, err := depbot.Run(&ghForge{base: *base}, depbot.Options{
		Required: lines(os.Getenv("DEPBOT_REQUIRED_CHECKS")),
		DryRun:   *dryRun,
		Log:      os.Stdout,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "qg-depbot:", err)
		os.Exit(1)
	}
}

func policyFromStdin() int {
	diff, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qg-depbot:", err)
		return 2
	}
	p := depbot.EvaluateDiff(string(diff))
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(p)
	if !p.Eligible {
		return 1
	}
	return 0
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// dependabotLogin is how gh reports the Dependabot GitHub App as an author.
const dependabotLogin = "app/dependabot"

// ghForge implements depbot.Forge with the gh CLI.
type ghForge struct {
	base string
}

func gh(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...) // #nosec G204 -- fixed binary; arguments are built here from PR numbers and SHAs returned by the GitHub API, never from a shell string
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func num(pr depbot.PR) string { return strconv.Itoa(pr.Number) }

func (f *ghForge) OpenPRs() ([]depbot.PR, error) {
	out, err := gh("pr", "list", "--state", "open", "--base", f.base, "--author", dependabotLogin,
		"--limit", "100", "--json", "number,title,state,headRefOid,labels,author,isCrossRepository,isDraft")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Number            int
		Title             string
		State             string
		HeadRefOid        string
		IsCrossRepository bool
		IsDraft           bool
		Author            struct {
			Login string
			IsBot bool `json:"is_bot"`
		}
		Labels []struct{ Name string }
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decode pull request list: %w", err)
	}
	var prs []depbot.PR
	for _, r := range raw {
		// Re-check what the query asked for: the author filter is a
		// search hint, and only Dependabot's own same-repository pull
		// requests may ever be merged from here.
		if r.State != "OPEN" || r.IsDraft || r.IsCrossRepository || !r.Author.IsBot || r.Author.Login != dependabotLogin {
			continue
		}
		pr := depbot.PR{Number: r.Number, Title: r.Title, HeadSHA: r.HeadRefOid}
		for _, l := range r.Labels {
			pr.Labels = append(pr.Labels, l.Name)
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

func (f *ghForge) Diff(pr depbot.PR) (string, error) {
	out, err := gh("pr", "diff", num(pr))
	return string(out), err
}

func (f *ghForge) Checks(pr depbot.PR) ([]depbot.Check, error) {
	out, err := gh("pr", "view", num(pr), "--json", "headRefOid,statusCheckRollup")
	if err != nil {
		return nil, err
	}
	var raw struct {
		HeadRefOid        string
		StatusCheckRollup []depbot.Check
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decode check rollup: %w", err)
	}
	if raw.HeadRefOid != pr.HeadSHA {
		return nil, fmt.Errorf("head moved from %s to %s during the pass", pr.HeadSHA, raw.HeadRefOid)
	}
	return raw.StatusCheckRollup, nil
}

func (f *ghForge) BehindBy(pr depbot.PR) (int, error) {
	out, err := gh("api", "repos/{owner}/{repo}/compare/"+f.base+"..."+pr.HeadSHA, "--jq", ".behind_by")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

func (f *ghForge) AddLabel(pr depbot.PR, label string) error {
	_, err := gh("pr", "edit", num(pr), "--add-label", label)
	return err
}

func (f *ghForge) UpdateBranch(pr depbot.PR) error {
	_, err := gh("api", "-X", "PUT", "repos/{owner}/{repo}/pulls/"+num(pr)+"/update-branch",
		"-f", "expected_head_sha="+pr.HeadSHA)
	return err
}

// Merge never passes --admin: gh refuses a pull request the branch rules
// do not allow, and --match-head-commit refuses one whose head is no
// longer the commit that was evaluated.
func (f *ghForge) Merge(pr depbot.PR) error {
	_, err := gh("pr", "merge", num(pr), "--squash", "--delete-branch", "--match-head-commit", pr.HeadSHA)
	return err
}

// Report keeps at most one marker comment per pull request. An unchanged
// body is left alone and a changed one is edited in place, so only the
// first appearance of a verdict notifies. An empty body retires the
// comment.
func (f *ghForge) Report(pr depbot.PR, body string) error {
	out, err := gh("api", "--paginate", "repos/{owner}/{repo}/issues/"+num(pr)+"/comments",
		"--jq", `.[] | select(.body | startswith("`+depbot.ReportMarker+`")) | {id, body}`)
	if err != nil {
		return err
	}
	var existing struct {
		ID   int64
		Body string
	}
	found := false
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		if err := dec.Decode(&existing); err != nil {
			return fmt.Errorf("decode comments: %w", err)
		}
		found = true
	}
	comment := "repos/{owner}/{repo}/issues/comments/" + strconv.FormatInt(existing.ID, 10)
	switch {
	case !found && body == "":
		return nil
	case !found:
		_, err = gh("api", "-X", "POST", "repos/{owner}/{repo}/issues/"+num(pr)+"/comments", "-f", "body="+body)
	case body == "":
		_, err = gh("api", "-X", "DELETE", comment)
	case strings.TrimSpace(existing.Body) != strings.TrimSpace(body):
		_, err = gh("api", "-X", "PATCH", comment, "-f", "body="+body)
	}
	return err
}
