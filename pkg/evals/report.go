/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package evals

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/diillson/chatcli/i18n"
)

// ReportSchema versions the report layout.
const ReportSchema = 1

// Report is the result of one run: per-case verdicts plus a summary, written
// as JSON so a later run can compare against it.
type Report struct {
	Schema         int          `json:"schema"`
	Version        string       `json:"chatcli_version,omitempty"`
	Suites         []string     `json:"suites"`
	StartedAt      time.Time    `json:"started_at"`
	FinishedAt     time.Time    `json:"finished_at"`
	DurationMS     int64        `json:"duration_ms"`
	Candidate      JudgeTarget  `json:"candidate"`
	Judge          *JudgeTarget `json:"judge,omitempty"`
	SelfJudged     bool         `json:"self_judged,omitempty"`
	WithMemory     bool         `json:"with_memory,omitempty"`
	BudgetExceeded bool         `json:"budget_exceeded,omitempty"`
	Summary        Summary      `json:"summary"`
	Cases          []CaseResult `json:"cases"`
}

// Summary aggregates the run.
type Summary struct {
	Cases        int     `json:"cases"`
	Passed       int     `json:"passed"`
	Failed       int     `json:"failed"`
	Errored      int     `json:"errored"`
	Skipped      int     `json:"skipped"`
	Flaky        int     `json:"flaky"`
	PassRate     float64 `json:"pass_rate"`
	MeanScore    float64 `json:"mean_score"`
	Trials       int     `json:"trials"`
	CostUSD      float64 `json:"cost_usd"`
	JudgeCostUSD float64 `json:"judge_cost_usd"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	P50MS        int64   `json:"p50_ms"`
	P95MS        int64   `json:"p95_ms"`
}

// CaseResult is one case's verdict across its trials.
type CaseResult struct {
	Suite        string        `json:"suite"`
	ID           string        `json:"id"`
	Description  string        `json:"description,omitempty"`
	Tags         []string      `json:"tags,omitempty"`
	Mode         string        `json:"mode"`
	PassPolicy   string        `json:"pass_policy"`
	Status       string        `json:"status"`
	SkipReason   string        `json:"skip_reason,omitempty"`
	PassRate     float64       `json:"pass_rate"`
	PassAtK      bool          `json:"pass_at_k"`
	PassHatK     bool          `json:"pass_hat_k"`
	Flaky        bool          `json:"flaky,omitempty"`
	MeanScore    float64       `json:"mean_score"`
	CostUSD      float64       `json:"cost_usd"`
	JudgeCostUSD float64       `json:"judge_cost_usd,omitempty"`
	Trials       []TrialResult `json:"trials,omitempty"`
}

// Key identifies a case across reports.
func (c *CaseResult) Key() string { return c.Suite + "/" + c.ID }

func aggregateCase(s *Suite, c *Case, trials []TrialResult) CaseResult {
	cr := CaseResult{
		Suite:       s.Name,
		ID:          c.ID,
		Description: c.Description,
		Tags:        c.Tags,
		Mode:        c.Mode,
		PassPolicy:  c.PassPolicy,
		Trials:      trials,
	}
	if c.Skip != "" {
		cr.Status = StatusSkipped
		cr.SkipReason = c.Skip
		return cr
	}
	var pass, fail, errs, ran int
	var scoreSum float64
	for _, t := range trials {
		cr.CostUSD += t.CostUSD
		cr.JudgeCostUSD += t.JudgeCostUSD
		switch t.Status {
		case StatusPass:
			pass++
		case StatusFail:
			fail++
		case StatusError:
			errs++
		default:
			continue // skipped (budget, cancel): not a run
		}
		ran++
		scoreSum += t.Score
	}
	if ran == 0 {
		cr.Status = StatusSkipped
		if len(trials) > 0 {
			cr.SkipReason = trials[0].Error
		}
		return cr
	}
	cr.PassRate = float64(pass) / float64(ran)
	cr.MeanScore = scoreSum / float64(ran)
	cr.PassAtK = pass > 0
	cr.PassHatK = pass == ran
	cr.Flaky = pass > 0 && pass < ran

	var ok bool
	switch c.PassPolicy {
	case PassAny:
		ok = cr.PassAtK
	case PassMajority:
		ok = pass*2 > ran
	default:
		ok = cr.PassHatK
	}
	switch {
	case ok:
		cr.Status = StatusPass
	case pass == 0 && fail == 0:
		cr.Status = StatusError // never got far enough to be graded
	default:
		cr.Status = StatusFail
	}
	return cr
}

func (r *Report) summarize() {
	s := Summary{}
	var scoreSum float64
	var graded int
	var durations []int64
	providers := map[string]bool{}
	for i := range r.Cases {
		c := &r.Cases[i]
		s.Cases++
		switch c.Status {
		case StatusPass:
			s.Passed++
		case StatusFail:
			s.Failed++
		case StatusError:
			s.Errored++
		case StatusSkipped:
			s.Skipped++
		}
		if c.Flaky {
			s.Flaky++
		}
		if c.Status != StatusSkipped {
			graded++
			scoreSum += c.MeanScore
		}
		s.CostUSD += c.CostUSD
		s.JudgeCostUSD += c.JudgeCostUSD
		for _, t := range c.Trials {
			if t.Status == StatusSkipped || t.Status == "" {
				continue
			}
			s.Trials++
			s.InputTokens += t.InputTokens
			s.OutputTokens += t.OutputTokens
			durations = append(durations, t.DurationMS)
			if t.Provider != "" || t.Model != "" {
				providers[t.Provider+"\x00"+t.Model] = true
				if r.Candidate.Provider == "" && r.Candidate.Model == "" {
					r.Candidate = JudgeTarget{Provider: t.Provider, Model: t.Model}
				}
			}
		}
	}
	if len(providers) > 1 {
		r.Candidate.Model = i18n.T("evals.report.mixed_models")
	}
	if graded > 0 {
		s.PassRate = float64(s.Passed) / float64(graded)
		s.MeanScore = scoreSum / float64(graded)
	}
	s.P50MS = percentile(durations, 50)
	s.P95MS = percentile(durations, 95)
	r.Summary = s
}

func percentile(xs []int64, p int) int64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := (p*len(s)+99)/100 - 1 // nearest-rank
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}

// WriteReport saves the report as indented JSON.
func WriteReport(path string, r *Report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// ReadReport loads a report written by WriteReport.
func ReadReport(path string) (*Report, error) {
	data, err := os.ReadFile(path) //#nosec G304 -- report path is the operator's own CLI argument
	if err != nil {
		return nil, errors.New(i18n.T("evals.load.not_found", path))
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, errors.New(i18n.T("evals.report.parse", path, err))
	}
	if r.Schema == 0 || r.Schema > ReportSchema {
		return nil, errors.New(i18n.T("evals.report.schema", path, r.Schema, ReportSchema))
	}
	return &r, nil
}

// MeetsPassRate reports whether the run reached the minimum pass rate.
func (r *Report) MeetsPassRate(floor float64) bool {
	return r.Summary.PassRate+1e-9 >= floor
}

// usd formats a dollar amount for humans.
func usd(v float64) string { return fmt.Sprintf("$%.4f", v) }
