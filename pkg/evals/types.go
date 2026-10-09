/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package evals is ChatCLI's offline evaluation harness: a fixed suite of
// cases, each one a prompt run end-to-end through the real chatcli binary in
// a disposable sandbox, graded by deterministic checks (text, files, commands,
// tool usage, cost) and, where judgment is needed, by an LLM judge with an
// explicit rubric. Results aggregate across trials (LLM output is
// nondeterministic) and compare against a stored baseline, so a prompt, model
// or engine change that makes ChatCLI worse fails CI instead of shipping.
//
// The package never imports the cli package: the candidate runs as a
// subprocess (Executor) and the judge is an injected Sender, which keeps the
// harness provider-agnostic and testable with fakes.
package evals

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Execution modes a case can run in.
const (
	ModeChat  = "chat"
	ModeCoder = "coder"
)

// Pass policies: how a case's trials combine into its verdict.
const (
	PassAll      = "all"      // every trial passes (pass^k) — the default, strict
	PassAny      = "any"      // at least one trial passes (pass@k)
	PassMajority = "majority" // more than half of the trials pass
)

// Case and trial statuses.
const (
	StatusPass    = "pass"
	StatusFail    = "fail"
	StatusError   = "error"
	StatusSkipped = "skipped"
)

// Duration is a time.Duration that unmarshals from YAML strings like "90s".
type Duration time.Duration

// UnmarshalYAML accepts Go duration strings ("2m30s") and bare seconds.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	s := strings.TrimSpace(n.Value)
	if s == "" {
		*d = 0
		return nil
	}
	if v, err := time.ParseDuration(s); err == nil {
		*d = Duration(v)
		return nil
	}
	var secs int
	if _, err := fmt.Sscanf(s, "%d", &secs); err == nil && fmt.Sprint(secs) == s {
		*d = Duration(time.Duration(secs) * time.Second)
		return nil
	}
	return fmt.Errorf("invalid duration %q (use e.g. 90s, 2m)", s)
}

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// PolicyRule is one coder security-policy rule installed in a coder case's
// sandbox (the workspace-local coder_policy.json, merged over the user's
// global policy). Safety-immune operations keep asking regardless — and an
// eval has no human to answer, so they are denied.
type PolicyRule struct {
	Pattern string `yaml:"pattern" json:"pattern"`
	Action  string `yaml:"action" json:"action"` // allow | deny | ask
}

// JudgeTarget names the model that grades judge checks.
type JudgeTarget struct {
	Provider string `yaml:"provider" json:"provider,omitempty"`
	Model    string `yaml:"model" json:"model,omitempty"`
}

// Defaults are suite-wide values a case inherits unless it sets its own.
type Defaults struct {
	Mode       string            `yaml:"mode"`
	Timeout    Duration          `yaml:"timeout"`
	Trials     int               `yaml:"trials"`
	PassPolicy string            `yaml:"pass_policy"`
	Env        map[string]string `yaml:"env"`
	Policy     []PolicyRule      `yaml:"policy"`
	Setup      []string          `yaml:"setup"`
	Checks     []Check           `yaml:"checks"` // appended to every case's checks
}

// Suite is one evaluation suite file.
type Suite struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Judge       JudgeTarget `yaml:"judge"`
	Defaults    Defaults    `yaml:"defaults"`
	Cases       []*Case     `yaml:"cases"`

	// Path is the file the suite was loaded from; Dir resolves fixtures.
	Path string `yaml:"-"`
	Dir  string `yaml:"-"`
}

// Case is one evaluation: a prompt, the workspace it runs in, and the checks
// its outcome must satisfy.
type Case struct {
	ID          string            `yaml:"id"`
	Description string            `yaml:"description"`
	Tags        []string          `yaml:"tags"`
	Mode        string            `yaml:"mode"`
	Prompt      string            `yaml:"prompt"`
	Fixture     string            `yaml:"fixture"` // directory copied into the sandbox
	Files       map[string]string `yaml:"files"`   // inline files written into the sandbox
	Setup       []string          `yaml:"setup"`   // shell commands run in the sandbox first
	Timeout     Duration          `yaml:"timeout"`
	Trials      int               `yaml:"trials"`
	PassPolicy  string            `yaml:"pass_policy"`
	Env         map[string]string `yaml:"env"`
	Policy      []PolicyRule      `yaml:"policy"`
	Skip        string            `yaml:"skip"` // non-empty: skipped, the value is the reason
	Checks      []Check           `yaml:"checks"`

	// FixtureDir is Fixture resolved against the suite directory.
	FixtureDir string `yaml:"-"`
}

// Check is one assertion. Exactly one kind field is set; Name, Weight and
// IgnoreCase modify it.
type Check struct {
	Name       string  `yaml:"name"`
	Weight     float64 `yaml:"weight"`
	IgnoreCase bool    `yaml:"ignore_case"`

	Contains      string        `yaml:"contains"`
	NotContains   string        `yaml:"not_contains"`
	Equals        string        `yaml:"equals"`
	Regex         string        `yaml:"regex"`
	NotRegex      string        `yaml:"not_regex"`
	JSON          *JSONCheck    `yaml:"json"`
	FileExists    string        `yaml:"file_exists"`
	FileAbsent    string        `yaml:"file_absent"`
	FileContains  *FileCheck    `yaml:"file_contains"`
	FileRegex     *FileCheck    `yaml:"file_regex"`
	Command       *CommandCheck `yaml:"command"`
	ToolCalled    string        `yaml:"tool_called"`
	ToolNotCalled string        `yaml:"tool_not_called"`
	MaxCostUSD    *float64      `yaml:"max_cost_usd"`
	MaxTurns      *int          `yaml:"max_turns"`
	MaxDuration   Duration      `yaml:"max_duration"`
	Judge         *JudgeCheck   `yaml:"judge"`
}

// JSONCheck requires the answer (or its first fenced/embedded JSON value) to
// parse as JSON and, optionally, to carry the listed top-level keys.
type JSONCheck struct {
	RequireKeys []string `yaml:"require_keys"`
}

// FileCheck asserts on a sandbox file's content.
type FileCheck struct {
	Path string `yaml:"path"`
	Text string `yaml:"text"`
}

// CommandCheck runs a shell command in the sandbox after the candidate ran
// (e.g. `go test ./...`) and asserts on its exit code and output.
type CommandCheck struct {
	Run        string   `yaml:"run"`
	ExpectExit int      `yaml:"expect_exit"`
	Contains   string   `yaml:"contains"`
	Timeout    Duration `yaml:"timeout"`
}

// JudgeCheck grades the answer with an LLM against a rubric.
type JudgeCheck struct {
	Rubric    string  `yaml:"rubric"`
	Reference string  `yaml:"reference"` // optional gold answer shown to the judge
	Threshold float64 `yaml:"threshold"` // score in [0,1] needed to pass; default 0.7
	Samples   int     `yaml:"samples"`   // judge calls, median score wins; default 1
}

// Check kinds, as reported in results.
const (
	KindContains      = "contains"
	KindNotContains   = "not_contains"
	KindEquals        = "equals"
	KindRegex         = "regex"
	KindNotRegex      = "not_regex"
	KindJSON          = "json"
	KindFileExists    = "file_exists"
	KindFileAbsent    = "file_absent"
	KindFileContains  = "file_contains"
	KindFileRegex     = "file_regex"
	KindCommand       = "command"
	KindToolCalled    = "tool_called"
	KindToolNotCalled = "tool_not_called"
	KindMaxCost       = "max_cost_usd"
	KindMaxTurns      = "max_turns"
	KindMaxDuration   = "max_duration"
	KindJudge         = "judge"
)

// Kinds returns every kind set on the check (valid checks have exactly one).
func (c *Check) Kinds() []string {
	var k []string
	add := func(set bool, kind string) {
		if set {
			k = append(k, kind)
		}
	}
	add(c.Contains != "", KindContains)
	add(c.NotContains != "", KindNotContains)
	add(c.Equals != "", KindEquals)
	add(c.Regex != "", KindRegex)
	add(c.NotRegex != "", KindNotRegex)
	add(c.JSON != nil, KindJSON)
	add(c.FileExists != "", KindFileExists)
	add(c.FileAbsent != "", KindFileAbsent)
	add(c.FileContains != nil, KindFileContains)
	add(c.FileRegex != nil, KindFileRegex)
	add(c.Command != nil, KindCommand)
	add(c.ToolCalled != "", KindToolCalled)
	add(c.ToolNotCalled != "", KindToolNotCalled)
	add(c.MaxCostUSD != nil, KindMaxCost)
	add(c.MaxTurns != nil, KindMaxTurns)
	add(c.MaxDuration != 0, KindMaxDuration)
	add(c.Judge != nil, KindJudge)
	return k
}

// Kind returns the check's single kind ("" when invalid).
func (c *Check) Kind() string {
	if k := c.Kinds(); len(k) == 1 {
		return k[0]
	}
	return ""
}

// Label is the human name of the check: Name when set, else kind + target.
func (c *Check) Label() string {
	if c.Name != "" {
		return c.Name
	}
	target := ""
	switch c.Kind() {
	case KindContains:
		target = c.Contains
	case KindNotContains:
		target = c.NotContains
	case KindEquals:
		target = c.Equals
	case KindRegex:
		target = c.Regex
	case KindNotRegex:
		target = c.NotRegex
	case KindFileExists:
		target = c.FileExists
	case KindFileAbsent:
		target = c.FileAbsent
	case KindFileContains:
		target = c.FileContains.Path
	case KindFileRegex:
		target = c.FileRegex.Path
	case KindCommand:
		target = c.Command.Run
	case KindToolCalled:
		target = c.ToolCalled
	case KindToolNotCalled:
		target = c.ToolNotCalled
	case KindMaxCost:
		target = fmt.Sprintf("%.4f", *c.MaxCostUSD)
	case KindMaxTurns:
		target = fmt.Sprint(*c.MaxTurns)
	case KindMaxDuration:
		target = c.MaxDuration.D().String()
	case KindJudge:
		target = firstLine(c.Judge.Rubric, 60)
	}
	return c.Kind() + " " + truncate(target, 60)
}

// weight returns the check's effective weight (default 1).
func (c *Check) weight() float64 {
	if c.Weight > 0 {
		return c.Weight
	}
	return 1
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncate(s, n)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
