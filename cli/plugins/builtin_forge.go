/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * @forge — git-forge operations (pull requests, issues, CI) through the
 * user's own authenticated forge CLI: gh for GitHub, glab for GitLab. The
 * daily loop "branch → PR → watch CI → fix" no longer stops at local git.
 *
 * Keyless by design: ChatCLI never stores forge credentials — it shells out
 * to the CLI the user already logged into. The forge is auto-detected from
 * the git remote (gitlab remotes route to glab), overridable per call.
 * Output is capped; read operations (list/view/diff/checks/status) are
 * read-only for the security gate, mutations (create/comment/merge) are not.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	forgeTimeout   = 60 * time.Second
	forgeMaxOutput = 16_000
)

// forgeRunner executes the forge CLI. Package variable so tests can fake the
// binary entirely.
var forgeRunner = func(ctx context.Context, bin string, args []string) (string, error) {
	if _, err := exec.LookPath(bin); err != nil {
		return "", fmt.Errorf("%s CLI not found — install it and authenticate (%s auth login) to use @forge", bin, bin)
	}
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput() // #nosec G204 -- bin is a fixed forge CLI name, args built from a validated allowlist
	text := strings.TrimSpace(string(out))
	if len(text) > forgeMaxOutput {
		text = text[:forgeMaxOutput] + "\n… (output truncated)"
	}
	if err != nil {
		if text == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, text)
	}
	return text, nil
}

// forgeRemoteURL reads the origin remote for forge auto-detection. Package
// variable for tests.
var forgeRemoteURL = func(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// BuiltinForgePlugin is the @forge tool.
type BuiltinForgePlugin struct{}

// NewBuiltinForgePlugin returns a ready-to-register plugin.
func NewBuiltinForgePlugin() *BuiltinForgePlugin { return &BuiltinForgePlugin{} }

// Name returns "@forge".
func (*BuiltinForgePlugin) Name() string { return "@forge" }

// Description surfaces the tool in /plugin list and the agent tool catalog.
func (*BuiltinForgePlugin) Description() string {
	return "Git-forge operations through the user's authenticated gh (GitHub) or glab (GitLab) CLI: list/view/diff pull requests, watch their CI checks and read failing run logs, list/view issues, and — behind confirmation — create PRs and post comments. The forge is auto-detected from the git remote. Use it to close the loop branch -> PR -> CI -> fix without leaving the agent."
}

// Usage explains the canonical invocation forms.
func (*BuiltinForgePlugin) Usage() string {
	return `<tool_call name="@forge" args='{"cmd":"pr-checks","args":{"number":42}}' />

Subcommands:
  pr-list [--limit N]              open pull requests
  pr-view {number}                 one PR: title, body, state, reviews
  pr-diff {number}                 the PR's diff (capped)
  pr-checks {number}               CI check status for the PR
  pr-create --title T [--body B] [--base BR] [--draft]   create a PR from the current branch
  pr-comment {number} --body B     comment on a PR
  issue-list [--limit N]           open issues
  issue-view {number}              one issue with its discussion
  issue-comment {number} --body B  comment on an issue
  ci-status [--branch BR]          latest workflow runs (current branch by default)
  ci-logs {run-id}                 the failed steps' logs of a run
  detect                           which forge CLI would be used (gh or glab) and why

GitLab remotes route to glab automatically; pass {"host":"github"} or {"host":"gitlab"} to force.`
}

// Version returns the plugin contract version.
func (*BuiltinForgePlugin) Version() string { return "1.0.0" }

// Path identifies the plugin as builtin.
func (*BuiltinForgePlugin) Path() string { return "[builtin]" }

// Schema declares the machine-readable command surface.
func (*BuiltinForgePlugin) Schema() string {
	schema := map[string]interface{}{
		"name":        "@forge",
		"description": "Pull requests, issues and CI via the user's authenticated gh/glab CLI.",
		"commands": []map[string]interface{}{
			{"name": "pr-list", "description": "open pull requests", "examples": []string{`{"cmd":"pr-list"}`}},
			{"name": "pr-view", "description": "one PR in detail", "examples": []string{`{"cmd":"pr-view","args":{"number":42}}`}},
			{"name": "pr-diff", "description": "the PR's diff", "examples": []string{`{"cmd":"pr-diff","args":{"number":42}}`}},
			{"name": "pr-checks", "description": "CI check status for a PR", "examples": []string{`{"cmd":"pr-checks","args":{"number":42}}`}},
			{"name": "pr-create", "description": "create a PR from the current branch", "examples": []string{`{"cmd":"pr-create","args":{"title":"fix: x","body":"...","base":"main"}}`}},
			{"name": "pr-comment", "description": "comment on a PR", "examples": []string{`{"cmd":"pr-comment","args":{"number":42,"body":"..."}}`}},
			{"name": "issue-list", "description": "open issues", "examples": []string{`{"cmd":"issue-list"}`}},
			{"name": "issue-view", "description": "one issue with discussion", "examples": []string{`{"cmd":"issue-view","args":{"number":7}}`}},
			{"name": "issue-comment", "description": "comment on an issue", "examples": []string{`{"cmd":"issue-comment","args":{"number":7,"body":"..."}}`}},
			{"name": "ci-status", "description": "latest workflow runs", "examples": []string{`{"cmd":"ci-status"}`}},
			{"name": "ci-logs", "description": "failed-step logs of a run", "examples": []string{`{"cmd":"ci-logs","args":{"run":123456}}`}},
			{"name": "detect", "description": "which forge CLI would be used", "examples": []string{`{"cmd":"detect"}`}},
		},
	}
	b, _ := json.MarshalIndent(schema, "", "  ")
	return string(b)
}

// forgeInvocation is one parsed @forge call.
type forgeInvocation struct {
	cmd    string
	number string
	title  string
	body   string
	base   string
	branch string
	run    string
	host   string
	limit  int
	draft  bool
}

// forgeMutatingCmds require the security gate.
var forgeMutatingCmds = map[string]bool{
	"pr-create":     true,
	"pr-comment":    true,
	"issue-comment": true,
}

// Execute dispatches a @forge invocation.
func (p *BuiltinForgePlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream dispatches a @forge invocation (no streaming).
func (p *BuiltinForgePlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	inv, err := parseForgeInvocation(args)
	if err != nil {
		return "", err
	}
	opCtx, cancel := context.WithTimeout(ctx, forgeTimeout)
	defer cancel()

	bin, why := detectForgeCLI(opCtx, inv.host)
	if inv.cmd == "detect" {
		return fmt.Sprintf(forgeMsgDetectFmt, bin, why), nil
	}
	cliArgs, err := buildForgeArgs(bin, inv)
	if err != nil {
		return "", err
	}
	out, err := forgeRunner(opCtx, bin, cliArgs)
	if err != nil {
		return "", fmt.Errorf("@forge %s: %w", inv.cmd, err)
	}
	if strings.TrimSpace(out) == "" {
		out = "(no output)"
	}
	return out, nil
}

// detectForgeCLI picks gh or glab: explicit host wins, then the origin
// remote, defaulting to gh.
func detectForgeCLI(ctx context.Context, host string) (bin, why string) {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "github", "gh":
		return "gh", "forced by host argument"
	case "gitlab", "glab":
		return "glab", "forced by host argument"
	}
	remote := forgeRemoteURL(ctx)
	if strings.Contains(remote, "gitlab") {
		return "glab", "origin remote is a GitLab URL"
	}
	if remote == "" {
		return "gh", "no origin remote; defaulting to GitHub"
	}
	return "gh", "origin remote: " + remote
}

// forgeMsgDetectFmt is the model-facing detect result (named per house style).
const forgeMsgDetectFmt = "Forge CLI: %s (%s)"

// buildForgeArgs maps one invocation to the forge CLI's argv. Commands are an
// allowlist — anything else was already rejected by the parser. Split per
// family to keep each mapper trivially readable.
func buildForgeArgs(bin string, inv forgeInvocation) ([]string, error) {
	switch {
	case strings.HasPrefix(inv.cmd, "pr-"):
		return buildForgePRArgs(bin, inv)
	case strings.HasPrefix(inv.cmd, "issue-"):
		return buildForgeIssueArgs(bin, inv)
	case strings.HasPrefix(inv.cmd, "ci-"):
		return buildForgeCIArgs(bin, inv)
	default:
		return nil, fmt.Errorf("@forge: unknown cmd %q (valid: pr-list|pr-view|pr-diff|pr-checks|pr-create|pr-comment|issue-list|issue-view|issue-comment|ci-status|ci-logs|detect)", inv.cmd)
	}
}

// forgeNeedNumber validates the target number common to view/diff/comment.
func forgeNeedNumber(inv forgeInvocation) error {
	if strings.TrimSpace(inv.number) == "" {
		return fmt.Errorf("@forge %s: missing number", inv.cmd)
	}
	return nil
}

// forgeLimit normalizes the list limit.
func forgeLimit(inv forgeInvocation) string {
	if inv.limit <= 0 {
		return "20"
	}
	return fmt.Sprint(inv.limit)
}

// buildForgePRArgs maps the pr-* family.
func buildForgePRArgs(bin string, inv forgeInvocation) ([]string, error) {
	gh := bin == "gh"
	switch inv.cmd {
	case "pr-list":
		return []string{"pr", "list", "--limit", forgeLimit(inv)}, nil
	case "pr-view":
		if err := forgeNeedNumber(inv); err != nil {
			return nil, err
		}
		if gh {
			return []string{"pr", "view", inv.number, "--comments"}, nil
		}
		return []string{"mr", "view", inv.number}, nil
	case "pr-diff":
		if err := forgeNeedNumber(inv); err != nil {
			return nil, err
		}
		if gh {
			return []string{"pr", "diff", inv.number}, nil
		}
		return []string{"mr", "diff", inv.number}, nil
	case "pr-checks":
		if err := forgeNeedNumber(inv); err != nil {
			return nil, err
		}
		if gh {
			return []string{"pr", "checks", inv.number}, nil
		}
		return []string{"ci", "status", "--live=false"}, nil
	case "pr-create":
		return buildForgePRCreateArgs(gh, inv)
	case "pr-comment":
		if err := forgeNeedNumber(inv); err != nil {
			return nil, err
		}
		if strings.TrimSpace(inv.body) == "" {
			return nil, errors.New("@forge pr-comment: missing body")
		}
		if gh {
			return []string{"pr", "comment", inv.number, "--body", inv.body}, nil
		}
		return []string{"mr", "note", inv.number, "--message", inv.body}, nil
	}
	return nil, fmt.Errorf("@forge: unknown cmd %q", inv.cmd)
}

// buildForgePRCreateArgs maps pr-create for both CLIs.
func buildForgePRCreateArgs(gh bool, inv forgeInvocation) ([]string, error) {
	if strings.TrimSpace(inv.title) == "" {
		return nil, errors.New("@forge pr-create: missing title")
	}
	var args []string
	if gh {
		args = []string{"pr", "create", "--title", inv.title, "--body", inv.body}
		if inv.base != "" {
			args = append(args, "--base", inv.base)
		}
	} else {
		args = []string{"mr", "create", "--title", inv.title, "--description", inv.body}
		if inv.base != "" {
			args = append(args, "--target-branch", inv.base)
		}
	}
	if inv.draft {
		args = append(args, "--draft")
	}
	return args, nil
}

// buildForgeIssueArgs maps the issue-* family.
func buildForgeIssueArgs(bin string, inv forgeInvocation) ([]string, error) {
	gh := bin == "gh"
	switch inv.cmd {
	case "issue-list":
		return []string{"issue", "list", "--limit", forgeLimit(inv)}, nil
	case "issue-view":
		if err := forgeNeedNumber(inv); err != nil {
			return nil, err
		}
		if gh {
			return []string{"issue", "view", inv.number, "--comments"}, nil
		}
		return []string{"issue", "view", inv.number}, nil
	case "issue-comment":
		if err := forgeNeedNumber(inv); err != nil {
			return nil, err
		}
		if strings.TrimSpace(inv.body) == "" {
			return nil, errors.New("@forge issue-comment: missing body")
		}
		if gh {
			return []string{"issue", "comment", inv.number, "--body", inv.body}, nil
		}
		return []string{"issue", "note", inv.number, "--message", inv.body}, nil
	}
	return nil, fmt.Errorf("@forge: unknown cmd %q", inv.cmd)
}

// buildForgeCIArgs maps the ci-* family.
func buildForgeCIArgs(bin string, inv forgeInvocation) ([]string, error) {
	gh := bin == "gh"
	switch inv.cmd {
	case "ci-status":
		if gh {
			args := []string{"run", "list", "--limit", forgeLimit(inv)}
			if inv.branch != "" {
				args = append(args, "--branch", inv.branch)
			}
			return args, nil
		}
		return []string{"ci", "status", "--live=false"}, nil
	case "ci-logs":
		if strings.TrimSpace(inv.run) == "" {
			return nil, errors.New("@forge ci-logs: missing run id (from ci-status)")
		}
		if gh {
			return []string{"run", "view", inv.run, "--log-failed"}, nil
		}
		return []string{"ci", "trace", inv.run}, nil
	}
	return nil, fmt.Errorf("@forge: unknown cmd %q", inv.cmd)
}

// parseForgeInvocation understands the JSON envelope and flat argv forms,
// leniently. Aliases: "pr view 42" ≡ "pr-view 42".
func parseForgeInvocation(args []string) (forgeInvocation, error) {
	var inv forgeInvocation
	if len(args) == 0 {
		return inv, errors.New(`@forge: empty args. Example: <tool_call name="@forge" args='{"cmd":"pr-checks","args":{"number":42}}' />`)
	}
	payload := strings.TrimSpace(strings.Join(args, " "))
	if strings.HasPrefix(payload, "{") {
		return parseForgeEnvelope(payload)
	}

	inv.cmd = strings.ToLower(strings.TrimSpace(args[0]))
	rest := args[1:]
	// Two-token spelling ("pr view 42") folds into the canonical hyphenated cmd.
	if (inv.cmd == "pr" || inv.cmd == "issue" || inv.cmd == "ci") && len(rest) > 0 {
		inv.cmd = inv.cmd + "-" + strings.ToLower(rest[0])
		rest = rest[1:]
	}

	// The agent loop flattens {"cmd":"pr-view","args":{"number":42}} into argv
	// ["pr-view","--number","42"], so every args-map key arrives as a flag.
	// splitFlatArgs collects them generically; number/run must be recognized
	// here or they'd be mistaken for the positional target.
	flags, bools, positionals := splitFlatArgs(rest)
	inv.draft = bools["draft"]
	inv.title = firstFlag(flags, "title")
	inv.body = firstFlag(flags, "body")
	inv.base = firstFlag(flags, "base")
	inv.branch = firstFlag(flags, "branch")
	inv.host = firstFlag(flags, "host")
	inv.number = firstFlag(flags, "number", "id", "pr", "mr")
	inv.run = firstFlag(flags, "run", "run-id", "runid")
	if v := firstFlag(flags, "limit"); v != "" {
		inv.limit = forgeAtoi(v, 0)
	}
	if len(positionals) > 0 {
		if inv.cmd == "ci-logs" {
			if inv.run == "" {
				inv.run = positionals[0]
			}
		} else if inv.number == "" {
			inv.number = positionals[0]
		}
	}
	return inv, nil
}

// parseForgeEnvelope handles {"cmd":..., "args":{...}} with args optionally
// inlined at the top level.
func parseForgeEnvelope(payload string) (forgeInvocation, error) {
	var inv forgeInvocation
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return inv, fmt.Errorf(`@forge: parse envelope: %w. Expected {"cmd":"pr-view","args":{"number":42}}`, err)
	}
	var cmd string
	if rc, ok := raw["cmd"]; ok {
		_ = json.Unmarshal(rc, &cmd)
	}
	inv.cmd = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(cmd, " ", "-")))
	inner := raw
	if ra, ok := raw["args"]; ok && len(ra) > 0 && strings.TrimSpace(string(ra)) != "null" {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(ra, &nested); err == nil {
			inner = nested
		}
	}
	getStr := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := inner[k]; ok {
				var s string
				if json.Unmarshal(v, &s) == nil {
					return s
				}
				var n float64
				if json.Unmarshal(v, &n) == nil {
					return fmt.Sprint(int64(n))
				}
			}
		}
		return ""
	}
	getBool := func(key string) bool {
		if v, ok := inner[key]; ok {
			var b bool
			if json.Unmarshal(v, &b) == nil {
				return b
			}
		}
		return false
	}
	inv.number = getStr("number", "pr", "id", "mr")
	inv.title = getStr("title")
	inv.body = getStr("body", "description", "message")
	inv.base = getStr("base", "target")
	inv.branch = getStr("branch")
	inv.run = getStr("run", "run_id", "runId")
	inv.host = getStr("host", "forge")
	inv.limit = int(0)
	if v, ok := inner["limit"]; ok {
		var n int
		if json.Unmarshal(v, &n) == nil {
			inv.limit = n
		}
	}
	inv.draft = getBool("draft")
	return inv, nil
}

// forgeAtoi parses n leniently, falling back to def.
func forgeAtoi(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}
