/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
)

// DefaultCommandTimeout bounds a command check that sets no timeout.
const DefaultCommandTimeout = 2 * time.Minute

// maxCommandOutput caps the command output kept for a check's detail.
const maxCommandOutput = 64 << 10

// Outcome is everything a check can look at after one trial ran.
type Outcome struct {
	Prompt   string
	Final    string
	Record   *Record
	Duration time.Duration
	Sandbox  string
	Env      map[string]string
}

// CheckResult is one check's verdict on one trial.
type CheckResult struct {
	Kind   string  `json:"kind"`
	Label  string  `json:"label"`
	Passed bool    `json:"passed"`
	Score  float64 `json:"score"`
	Weight float64 `json:"weight"`
	Detail string  `json:"detail,omitempty"`
	// CostUSD is what grading itself spent (judge calls).
	CostUSD float64 `json:"cost_usd,omitempty"`
}

// Judge grades an answer against a rubric. The harness ships LLMJudge; tests
// inject fakes.
type Judge interface {
	Grade(ctx context.Context, req JudgeRequest) (JudgeVerdict, error)
}

// evaluate runs one check against an outcome.
func evaluate(ctx context.Context, c *Check, out *Outcome, judge Judge) CheckResult {
	res := CheckResult{Kind: c.Kind(), Label: c.Label(), Weight: c.weight()}
	pass := func(ok bool, detail string) CheckResult {
		res.Passed = ok
		if ok {
			res.Score = 1
		} else {
			res.Detail = detail // the failure explanation; a pass needs none
		}
		return res
	}

	switch res.Kind {
	case KindContains:
		return pass(containsText(out.Final, c.Contains, c.IgnoreCase),
			i18n.T("evals.check.contains.missing", c.Contains))
	case KindNotContains:
		return pass(!containsText(out.Final, c.NotContains, c.IgnoreCase),
			i18n.T("evals.check.not_contains.found", c.NotContains))
	case KindEquals:
		got, want := strings.TrimSpace(out.Final), strings.TrimSpace(c.Equals)
		ok := got == want || (c.IgnoreCase && strings.EqualFold(got, want))
		return pass(ok, i18n.T("evals.check.equals.diff", truncate(got, 120)))
	case KindRegex, KindNotRegex:
		expr := c.Regex
		if res.Kind == KindNotRegex {
			expr = c.NotRegex
		}
		re, err := compileRegex(expr, c.IgnoreCase)
		if err != nil {
			return pass(false, err.Error())
		}
		found := re.MatchString(out.Final)
		if res.Kind == KindRegex {
			return pass(found, i18n.T("evals.check.regex.missing", expr))
		}
		return pass(!found, i18n.T("evals.check.not_regex.found", re.FindString(out.Final)))
	case KindJSON:
		return checkJSON(c.JSON, out.Final, pass)
	case KindFileExists, KindFileAbsent:
		rel := c.FileExists
		if res.Kind == KindFileAbsent {
			rel = c.FileAbsent
		}
		p, err := SandboxPath(out.Sandbox, rel)
		if err != nil {
			return pass(false, err.Error())
		}
		_, statErr := os.Stat(p)
		if res.Kind == KindFileExists {
			return pass(statErr == nil, i18n.T("evals.check.file.missing", rel))
		}
		return pass(errors.Is(statErr, os.ErrNotExist), i18n.T("evals.check.file.present", rel))
	case KindFileContains, KindFileRegex:
		return checkFile(c, out.Sandbox, pass)
	case KindCommand:
		return checkCommand(ctx, c.Command, out, pass)
	case KindToolCalled:
		return pass(toolCalled(out.Record, c.ToolCalled),
			i18n.T("evals.check.tool.missing", c.ToolCalled, strings.Join(out.Record.ToolNames(), ", ")))
	case KindToolNotCalled:
		return pass(!toolCalled(out.Record, c.ToolNotCalled),
			i18n.T("evals.check.tool.called", c.ToolNotCalled))
	case KindMaxCost:
		cost := recordCost(out.Record)
		return pass(cost <= *c.MaxCostUSD, i18n.T("evals.check.budget.cost", cost, *c.MaxCostUSD))
	case KindMaxTurns:
		turns := 0
		if out.Record != nil {
			turns = out.Record.Turns
		}
		return pass(turns <= *c.MaxTurns, i18n.T("evals.check.budget.turns", turns, *c.MaxTurns))
	case KindMaxDuration:
		return pass(out.Duration <= c.MaxDuration.D(),
			i18n.T("evals.check.budget.duration", out.Duration.Round(time.Millisecond).String(), c.MaxDuration.D().String()))
	case KindJudge:
		return checkJudge(ctx, c, out, judge, res)
	}
	return pass(false, i18n.T("evals.load.check_none"))
}

func containsText(haystack, needle string, ignoreCase bool) bool {
	if ignoreCase {
		return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
	}
	return strings.Contains(haystack, needle)
}

func recordCost(r *Record) float64 {
	if r == nil {
		return 0
	}
	return r.CostUSD
}

// toolCalled matches a tool by name ("@coder") or by name plus the start of
// its arguments ("@coder exec"), case-insensitively.
func toolCalled(r *Record, want string) bool {
	if r == nil {
		return false
	}
	want = strings.ToLower(strings.TrimSpace(want))
	for _, tc := range r.ToolCalls {
		name := strings.ToLower(tc.Name)
		if name == want {
			return true
		}
		full := strings.ToLower(strings.TrimSpace(tc.Name + " " + normalizeToolArgs(tc.Args)))
		if strings.HasPrefix(full, want) {
			return true
		}
	}
	return false
}

// normalizeToolArgs flattens a JSON envelope ({"cmd":"exec",...}) into its
// subcommand so "@coder exec" matches both argv and JSON tool-call forms.
func normalizeToolArgs(args string) string {
	args = strings.TrimSpace(args)
	if !strings.HasPrefix(args, "{") {
		return args
	}
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) != nil {
		return args
	}
	for _, k := range []string{"cmd", "command", "action", "subcommand"} {
		if v, ok := m[k].(string); ok {
			return v
		}
	}
	return args
}

func checkJSON(spec *JSONCheck, text string, pass func(bool, string) CheckResult) CheckResult {
	raw, ok := extractJSON(text)
	if !ok {
		return pass(false, i18n.T("evals.check.json.invalid"))
	}
	if len(spec.RequireKeys) == 0 {
		return pass(true, "")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return pass(false, i18n.T("evals.check.json.not_object"))
	}
	var missing []string
	for _, k := range spec.RequireKeys {
		if _, ok := obj[k]; !ok {
			missing = append(missing, k)
		}
	}
	return pass(len(missing) == 0, i18n.T("evals.check.json.keys", strings.Join(missing, ", ")))
}

// extractJSON returns the answer as JSON: the whole text, else the first
// ```json fence, else the first balanced object or array embedded in prose.
func extractJSON(text string) (json.RawMessage, bool) {
	t := strings.TrimSpace(text)
	if json.Valid([]byte(t)) && t != "" {
		return json.RawMessage(t), true
	}
	if i := strings.Index(t, "```"); i >= 0 {
		rest := t[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
			if j := strings.Index(rest, "```"); j >= 0 {
				body := strings.TrimSpace(rest[:j])
				if json.Valid([]byte(body)) && body != "" {
					return json.RawMessage(body), true
				}
			}
		}
	}
	for i := 0; i < len(t); i++ {
		if t[i] != '{' && t[i] != '[' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(t[i:]))
		var v json.RawMessage
		if dec.Decode(&v) == nil {
			return v, true
		}
	}
	return nil, false
}

func checkFile(c *Check, sandbox string, pass func(bool, string) CheckResult) CheckResult {
	fc := c.FileContains
	if fc == nil {
		fc = c.FileRegex
	}
	p, err := SandboxPath(sandbox, fc.Path)
	if err != nil {
		return pass(false, err.Error())
	}
	data, err := os.ReadFile(p) //#nosec G304 -- confined to the sandbox by SandboxPath
	if err != nil {
		return pass(false, i18n.T("evals.check.file.missing", fc.Path))
	}
	if c.FileContains != nil {
		return pass(containsText(string(data), fc.Text, c.IgnoreCase), i18n.T("evals.check.file.text", fc.Path, fc.Text))
	}
	re, err := compileRegex(fc.Text, c.IgnoreCase)
	if err != nil {
		return pass(false, err.Error())
	}
	return pass(re.Match(data), i18n.T("evals.check.file.text", fc.Path, fc.Text))
}

func checkCommand(ctx context.Context, cc *CommandCheck, out *Outcome, pass func(bool, string) CheckResult) CheckResult {
	timeout := cc.Timeout.D()
	if timeout <= 0 {
		timeout = DefaultCommandTimeout
	}
	code, output, err := RunShell(ctx, cc.Run, out.Sandbox, out.Env, timeout)
	if err != nil {
		return pass(false, err.Error())
	}
	if code != cc.ExpectExit {
		return pass(false, i18n.T("evals.check.command.exit", code, cc.ExpectExit, tail(output, 600)))
	}
	if cc.Contains != "" && !strings.Contains(output, cc.Contains) {
		return pass(false, i18n.T("evals.check.command.output", cc.Contains, tail(output, 600)))
	}
	return pass(true, "")
}

// RunShell runs a command line through the platform shell in dir and returns
// its exit code and combined output. A non-zero exit is not an error; failing
// to start, or running past the timeout, is.
func RunShell(ctx context.Context, line, dir string, env map[string]string, timeout time.Duration) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", line) //#nosec G204 -- suite-authored check command, run inside the sandbox
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", line) //#nosec G204 -- suite-authored check command, run inside the sandbox
	}
	cmd.Dir = dir
	cmd.Env = mergedEnviron(env)
	var buf limitedBuffer
	buf.max = maxCommandOutput
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return -1, buf.String(), errors.New(i18n.T("evals.check.command.timeout", line, timeout.String()))
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), buf.String(), nil
		}
		return -1, buf.String(), fmt.Errorf("%s: %w", line, err)
	}
	return 0, buf.String(), nil
}

// mergedEnviron is the parent environment with overrides applied.
func mergedEnviron(over map[string]string) []string {
	env := os.Environ()
	if len(over) == 0 {
		return env
	}
	out := make([]string, 0, len(env)+len(over))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if _, replaced := over[k]; !replaced {
			out = append(out, kv)
		}
	}
	for k, v := range over {
		out = append(out, k+"="+v)
	}
	return out
}

// limitedBuffer keeps the head of a stream up to max bytes and counts the rest.
type limitedBuffer struct {
	buf     bytes.Buffer
	max     int
	dropped int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	room := l.max - l.buf.Len()
	if room > 0 {
		if len(p) <= room {
			l.buf.Write(p)
		} else {
			l.buf.Write(p[:room])
			l.dropped += len(p) - room
		}
	} else {
		l.dropped += len(p)
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string {
	if l.dropped > 0 {
		return l.buf.String() + fmt.Sprintf("\n… [+%d bytes]", l.dropped)
	}
	return l.buf.String()
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}
