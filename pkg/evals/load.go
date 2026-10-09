/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package evals

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"gopkg.in/yaml.v3"
)

// Limits that keep a typo from turning one run into a bill.
const (
	DefaultTimeout   = 5 * time.Minute
	MaxTrials        = 20
	MaxJudgeSamples  = 5
	DefaultThreshold = 0.7
)

var caseIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Load reads one suite file, or every *.yaml / *.yml file directly inside a
// directory (not recursive: fixture trees may carry YAML of their own), and
// returns the validated suites with defaults applied to every case.
func Load(path string) ([]*Suite, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, errors.New(i18n.T("evals.load.not_found", path))
	}
	var files []string
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if !e.IsDir() && (ext == ".yaml" || ext == ".yml") {
				files = append(files, filepath.Join(path, e.Name()))
			}
		}
		sort.Strings(files)
		if len(files) == 0 {
			return nil, errors.New(i18n.T("evals.load.empty_dir", path))
		}
	} else {
		files = []string{path}
	}

	suites := make([]*Suite, 0, len(files))
	names := map[string]string{}
	for _, f := range files {
		s, err := LoadFile(f)
		if err != nil {
			return nil, err
		}
		if prev, dup := names[s.Name]; dup {
			return nil, errors.New(i18n.T("evals.load.duplicate_suite", s.Name, prev, f))
		}
		names[s.Name] = f
		suites = append(suites, s)
	}
	return suites, nil
}

// LoadFile parses and validates a single suite file.
func LoadFile(path string) (*Suite, error) {
	data, err := os.ReadFile(path) //#nosec G304 -- the suite path is the operator's own CLI argument
	if err != nil {
		return nil, errors.New(i18n.T("evals.load.not_found", path))
	}
	var s Suite
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // a misspelled key is an error, never a silently ignored check
	if err := dec.Decode(&s); err != nil && !errors.Is(err, io.EOF) {
		return nil, errors.New(i18n.T("evals.load.parse", path, err))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	s.Path = abs
	s.Dir = filepath.Dir(abs)
	if strings.TrimSpace(s.Name) == "" {
		s.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if err := s.normalize(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// normalize applies suite defaults to every case and validates the result.
func (s *Suite) normalize() error {
	d := s.Defaults
	if d.Mode == "" {
		d.Mode = ModeChat
	}
	if d.Timeout == 0 {
		d.Timeout = Duration(DefaultTimeout)
	}
	if d.Trials == 0 {
		d.Trials = 1
	}
	if d.PassPolicy == "" {
		d.PassPolicy = PassAll
	}
	if len(s.Cases) == 0 {
		return errors.New(i18n.T("evals.load.no_cases"))
	}
	if err := validatePolicy(d.Policy); err != nil {
		return err
	}

	seen := map[string]bool{}
	for i, c := range s.Cases {
		if c == nil {
			return errors.New(i18n.T("evals.load.case_empty", i+1))
		}
		if !caseIDPattern.MatchString(c.ID) {
			return errors.New(i18n.T("evals.load.case_id", i+1, c.ID))
		}
		if seen[c.ID] {
			return errors.New(i18n.T("evals.load.case_dup", c.ID))
		}
		seen[c.ID] = true

		if c.Mode == "" {
			c.Mode = d.Mode
		}
		if c.Timeout == 0 {
			c.Timeout = d.Timeout
		}
		if c.Trials == 0 {
			c.Trials = d.Trials
		}
		if c.PassPolicy == "" {
			c.PassPolicy = d.PassPolicy
		}
		c.Env = mergeEnv(d.Env, c.Env)
		if len(c.Policy) == 0 {
			c.Policy = append([]PolicyRule(nil), d.Policy...)
		}
		c.Setup = append(append([]string(nil), d.Setup...), c.Setup...)
		c.Checks = append(append([]Check(nil), c.Checks...), d.Checks...)

		if err := c.validate(s.Dir); err != nil {
			return fmt.Errorf("case %q: %w", c.ID, err)
		}
	}
	return nil
}

func mergeEnv(base, over map[string]string) map[string]string {
	if len(base) == 0 && len(over) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

func (c *Case) validate(suiteDir string) error {
	switch c.Mode {
	case ModeChat, ModeCoder:
	default:
		return errors.New(i18n.T("evals.load.mode", c.Mode))
	}
	if strings.TrimSpace(c.Prompt) == "" {
		return errors.New(i18n.T("evals.load.prompt"))
	}
	if c.Trials < 1 || c.Trials > MaxTrials {
		return errors.New(i18n.T("evals.load.trials", c.Trials, MaxTrials))
	}
	switch c.PassPolicy {
	case PassAll, PassAny, PassMajority:
	default:
		return errors.New(i18n.T("evals.load.pass_policy", c.PassPolicy))
	}
	if c.Timeout.D() < 0 {
		return errors.New(i18n.T("evals.load.timeout"))
	}
	if err := validatePolicy(c.Policy); err != nil {
		return err
	}
	if c.Fixture != "" {
		dir := c.Fixture
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(suiteDir, dir)
		}
		st, err := os.Stat(dir)
		if err != nil || !st.IsDir() {
			return errors.New(i18n.T("evals.load.fixture", c.Fixture))
		}
		c.FixtureDir = dir
	}
	for name := range c.Files {
		if _, err := SandboxPath("/sandbox", name); err != nil {
			return err
		}
	}
	if len(c.Checks) == 0 {
		return errors.New(i18n.T("evals.load.no_checks"))
	}
	for i := range c.Checks {
		if err := c.Checks[i].validate(); err != nil {
			return fmt.Errorf("check #%d: %w", i+1, err)
		}
	}
	return nil
}

func validatePolicy(rules []PolicyRule) error {
	for _, r := range rules {
		if strings.TrimSpace(r.Pattern) == "" {
			return errors.New(i18n.T("evals.load.policy_pattern"))
		}
		switch r.Action {
		case "allow", "deny", "ask":
		default:
			return errors.New(i18n.T("evals.load.policy_action", r.Action))
		}
	}
	return nil
}

func (c *Check) validate() error {
	kinds := c.Kinds()
	switch len(kinds) {
	case 0:
		return errors.New(i18n.T("evals.load.check_none"))
	case 1:
	default:
		return errors.New(i18n.T("evals.load.check_many", strings.Join(kinds, ", ")))
	}
	if c.Weight < 0 {
		return errors.New(i18n.T("evals.load.check_weight"))
	}
	for _, re := range []string{c.Regex, c.NotRegex} {
		if re != "" {
			if _, err := compileRegex(re, c.IgnoreCase); err != nil {
				return errors.New(i18n.T("evals.load.check_regex", re, err))
			}
		}
	}
	if err := c.validateFiles(); err != nil {
		return err
	}
	if c.Command != nil && strings.TrimSpace(c.Command.Run) == "" {
		return errors.New(i18n.T("evals.load.check_command"))
	}
	if (c.MaxCostUSD != nil && *c.MaxCostUSD < 0) || (c.MaxTurns != nil && *c.MaxTurns < 0) || c.MaxDuration < 0 {
		return errors.New(i18n.T("evals.load.check_budget"))
	}
	if c.Judge != nil {
		return c.Judge.normalize()
	}
	return nil
}

// validateFiles checks the sandbox paths and texts of file checks.
func (c *Check) validateFiles() error {
	for _, p := range []string{c.FileExists, c.FileAbsent} {
		if p != "" {
			if _, err := SandboxPath("/sandbox", p); err != nil {
				return err
			}
		}
	}
	for _, fc := range []*FileCheck{c.FileContains, c.FileRegex} {
		if fc == nil {
			continue
		}
		if _, err := SandboxPath("/sandbox", fc.Path); err != nil {
			return err
		}
		if fc.Text == "" {
			return errors.New(i18n.T("evals.load.check_file_text"))
		}
	}
	if c.FileRegex != nil {
		if _, err := compileRegex(c.FileRegex.Text, c.IgnoreCase); err != nil {
			return errors.New(i18n.T("evals.load.check_regex", c.FileRegex.Text, err))
		}
	}
	return nil
}

// normalize applies the judge defaults and validates the ranges.
func (j *JudgeCheck) normalize() error {
	if strings.TrimSpace(j.Rubric) == "" {
		return errors.New(i18n.T("evals.load.judge_rubric"))
	}
	if j.Threshold == 0 {
		j.Threshold = DefaultThreshold
	}
	if j.Threshold < 0 || j.Threshold > 1 {
		return errors.New(i18n.T("evals.load.judge_threshold", j.Threshold))
	}
	if j.Samples == 0 {
		j.Samples = 1
	}
	if j.Samples < 1 || j.Samples > MaxJudgeSamples {
		return errors.New(i18n.T("evals.load.judge_samples", j.Samples, MaxJudgeSamples))
	}
	return nil
}

func compileRegex(expr string, ignoreCase bool) (*regexp.Regexp, error) {
	if ignoreCase && !strings.HasPrefix(expr, "(?i)") {
		expr = "(?i)" + expr
	}
	return regexp.Compile(expr)
}

// SandboxPath joins a case-relative path onto root and refuses anything that
// would escape it (absolute paths, ".." climbing out). Suite files are code
// review material, but a check must never read or write outside its sandbox.
func SandboxPath(root, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", errors.New(i18n.T("evals.load.path_empty"))
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) || filepath.VolumeName(rel) != "" {
		return "", errors.New(i18n.T("evals.load.path_escape", rel))
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New(i18n.T("evals.load.path_escape", rel))
	}
	return filepath.Join(root, clean), nil
}

// Filter keeps the cases matching any of the selectors: a case id (glob
// patterns allowed, e.g. "coder-*") or "tag:<name>". No selectors keep all.
func Filter(suites []*Suite, selectors []string) []*Suite {
	var sel []string
	for _, s := range selectors {
		if s = strings.TrimSpace(s); s != "" {
			sel = append(sel, s)
		}
	}
	if len(sel) == 0 {
		return suites
	}
	out := make([]*Suite, 0, len(suites))
	for _, s := range suites {
		cp := *s
		cp.Cases = nil
		for _, c := range s.Cases {
			if caseMatches(c, sel) {
				cp.Cases = append(cp.Cases, c)
			}
		}
		if len(cp.Cases) > 0 {
			out = append(out, &cp)
		}
	}
	return out
}

func caseMatches(c *Case, selectors []string) bool {
	for _, s := range selectors {
		if tag, ok := strings.CutPrefix(s, "tag:"); ok {
			for _, t := range c.Tags {
				if strings.EqualFold(t, tag) {
					return true
				}
			}
			continue
		}
		if ok, _ := filepath.Match(s, c.ID); ok || s == c.ID {
			return true
		}
	}
	return false
}

// HasJudge reports whether any case in the suites uses an LLM judge check.
func HasJudge(suites []*Suite) bool {
	for _, s := range suites {
		for _, c := range s.Cases {
			for i := range c.Checks {
				if c.Checks[i].Judge != nil {
					return true
				}
			}
		}
	}
	return false
}
