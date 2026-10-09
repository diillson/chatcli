/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package evals

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
)

// statusMark is the one-glyph status shown in terminal output.
func statusMark(status string) string {
	switch status {
	case StatusPass:
		return "✓"
	case StatusFail:
		return "✗"
	case StatusError:
		return "!"
	}
	return "-"
}

func pct(v float64) string { return fmt.Sprintf("%.1f%%", v*100) }

func ms(v int64) string {
	return (time.Duration(v) * time.Millisecond).Round(100 * time.Millisecond).String()
}

// ProgressLine renders one finished trial for live terminal output.
func ProgressLine(ev TrialEvent) string {
	t := ev.Trial
	line := fmt.Sprintf("[%d/%d] %s %s/%s #%d", ev.Done, ev.Total, statusMark(t.Status), ev.Suite, ev.CaseID, t.N)
	if t.Status == StatusPass || t.Status == StatusFail {
		line += fmt.Sprintf("  %.2f · %s · %s", t.Score, ms(t.DurationMS), usd(t.CostUSD+t.JudgeCostUSD))
	}
	if t.Error != "" {
		line += "  " + firstLine(t.Error, 120)
	}
	return line
}

// WriteSummary renders the report for the terminal: one line per case, the
// failing checks of failed cases, then the totals.
func WriteSummary(w io.Writer, r *Report) {
	fmt.Fprintln(w)
	for i := range r.Cases {
		c := &r.Cases[i]
		line := fmt.Sprintf("%s %-40s %-6s", statusMark(c.Status), truncate(c.Key(), 40), c.Mode)
		switch c.Status {
		case StatusSkipped:
			line += "  " + i18n.T("evals.render.skipped", firstLine(c.SkipReason, 80))
		default:
			line += fmt.Sprintf("  %s  %.2f  %s", i18n.T("evals.render.trials_passed", passedTrials(c), ranTrials(c)), c.MeanScore, usd(c.CostUSD+c.JudgeCostUSD))
			if c.Flaky {
				line += "  " + i18n.T("evals.render.flaky")
			}
		}
		fmt.Fprintln(w, line)
		if c.Status == StatusFail || c.Status == StatusError {
			for _, d := range failureDetails(c, 3) {
				fmt.Fprintln(w, "    "+d)
			}
		}
	}
	s := r.Summary
	fmt.Fprintln(w)
	fmt.Fprintln(w, i18n.T("evals.render.totals", s.Passed, s.Cases-s.Skipped, pct(s.PassRate), s.Failed, s.Errored, s.Skipped, s.Flaky))
	fmt.Fprintln(w, i18n.T("evals.render.score_cost", s.MeanScore, usd(s.CostUSD), usd(s.JudgeCostUSD), s.InputTokens, s.OutputTokens))
	fmt.Fprintln(w, i18n.T("evals.render.latency", ms(s.P50MS), ms(s.P95MS), ms(r.DurationMS)))
	if r.Candidate.Provider != "" || r.Candidate.Model != "" {
		fmt.Fprintln(w, i18n.T("evals.render.candidate", strings.TrimSpace(r.Candidate.Provider+" "+r.Candidate.Model)))
	}
	if r.Judge != nil {
		fmt.Fprintln(w, i18n.T("evals.render.judge", strings.TrimSpace(r.Judge.Provider+" "+r.Judge.Model)))
	}
	if r.SelfJudged {
		fmt.Fprintln(w, i18n.T("evals.render.self_judged"))
	}
	if r.BudgetExceeded {
		fmt.Fprintln(w, i18n.T("evals.render.budget_exceeded"))
	}
}

func passedTrials(c *CaseResult) int {
	n := 0
	for _, t := range c.Trials {
		if t.Status == StatusPass {
			n++
		}
	}
	return n
}

func ranTrials(c *CaseResult) int {
	n := 0
	for _, t := range c.Trials {
		if t.Status != StatusSkipped && t.Status != "" {
			n++
		}
	}
	return n
}

// failureDetails lists why a case failed: trial errors and failing checks,
// deduplicated across trials, at most limit lines.
func failureDetails(c *CaseResult, limit int) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s == "" || seen[s] || len(out) >= limit {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, t := range c.Trials {
		if t.Error != "" {
			add("! " + firstLine(t.Error, 160))
		}
		for _, ck := range t.Checks {
			if !ck.Passed {
				add("✗ " + ck.Label + ": " + firstLine(ck.Detail, 140))
			}
		}
	}
	return out
}

// Markdown renders the report as a Markdown document (PR comments, CI
// summaries).
func Markdown(r *Report, cmp *Comparison) string {
	var b strings.Builder
	s := r.Summary
	fmt.Fprintf(&b, "# %s\n\n", i18n.T("evals.md.title", strings.Join(r.Suites, ", ")))
	fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n|---|---|---|---|---|---|\n",
		i18n.T("evals.md.col.pass_rate"), i18n.T("evals.md.col.score"), i18n.T("evals.md.col.cases"),
		i18n.T("evals.md.col.cost"), i18n.T("evals.md.col.p95"), i18n.T("evals.md.col.candidate"))
	fmt.Fprintf(&b, "| %s | %.2f | %d/%d | %s | %s | %s |\n\n",
		pct(s.PassRate), s.MeanScore, s.Passed, s.Cases-s.Skipped, usd(s.CostUSD+s.JudgeCostUSD), ms(s.P95MS),
		mdEscape(strings.TrimSpace(r.Candidate.Provider+" "+r.Candidate.Model)))
	if r.SelfJudged {
		fmt.Fprintf(&b, "> %s\n\n", i18n.T("evals.render.self_judged"))
	}
	if cmp != nil {
		fmt.Fprintf(&b, "## %s\n\n", i18n.T("evals.md.baseline"))
		fmt.Fprintf(&b, "- %s\n", i18n.T("evals.md.pass_rate_delta", pct(cmp.PassRateBefore), pct(cmp.PassRateAfter)))
		for _, d := range cmp.Regressions {
			fmt.Fprintf(&b, "- ❌ %s\n", i18n.T("evals.md.regression", "`"+d.Key+"`", d.Before, d.After))
		}
		for _, d := range cmp.Fixes {
			fmt.Fprintf(&b, "- ✅ %s\n", i18n.T("evals.md.fix", "`"+d.Key+"`", d.Before, d.After))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## %s\n\n| | %s | %s | %s | %s | %s |\n|---|---|---|---|---|---|\n",
		i18n.T("evals.md.cases"), i18n.T("evals.md.col.case"), i18n.T("evals.md.col.mode"),
		i18n.T("evals.md.col.trials"), i18n.T("evals.md.col.score"), i18n.T("evals.md.col.cost"))
	for i := range r.Cases {
		c := &r.Cases[i]
		fmt.Fprintf(&b, "| %s | `%s` | %s | %d/%d | %.2f | %s |\n", statusMark(c.Status), c.Key(), c.Mode,
			passedTrials(c), ranTrials(c), c.MeanScore, usd(c.CostUSD+c.JudgeCostUSD))
	}
	var failed []string
	for i := range r.Cases {
		c := &r.Cases[i]
		if c.Status != StatusFail && c.Status != StatusError {
			continue
		}
		var lines []string
		for _, d := range failureDetails(c, 5) {
			lines = append(lines, "  - "+mdEscape(d))
		}
		failed = append(failed, fmt.Sprintf("- `%s`\n%s", c.Key(), strings.Join(lines, "\n")))
	}
	if len(failed) > 0 {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", i18n.T("evals.md.failures"), strings.Join(failed, "\n"))
	}
	return b.String()
}

func mdEscape(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
}

// WriteComparison renders a baseline comparison for the terminal.
func WriteComparison(w io.Writer, c *Comparison) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, i18n.T("evals.render.cmp.header"))
	fmt.Fprintln(w, "  "+i18n.T("evals.md.pass_rate_delta", pct(c.PassRateBefore), pct(c.PassRateAfter)))
	fmt.Fprintln(w, "  "+i18n.T("evals.render.cmp.score", c.ScoreBefore, c.ScoreAfter))
	fmt.Fprintln(w, "  "+i18n.T("evals.render.cmp.cost", usd(c.CostBefore), usd(c.CostAfter)))
	for _, d := range c.Regressions {
		fmt.Fprintln(w, "  ✗ "+i18n.T("evals.md.regression", d.Key, d.Before, d.After))
	}
	for _, d := range c.Fixes {
		fmt.Fprintln(w, "  ✓ "+i18n.T("evals.md.fix", d.Key, d.Before, d.After))
	}
	if len(c.Added) > 0 {
		fmt.Fprintln(w, "  + "+i18n.T("evals.render.cmp.added", strings.Join(c.Added, ", ")))
	}
	if len(c.Removed) > 0 {
		fmt.Fprintln(w, "  - "+i18n.T("evals.render.cmp.removed", strings.Join(c.Removed, ", ")))
	}
}

// WriteCaseList renders `chatcli eval list`.
func WriteCaseList(w io.Writer, suites []*Suite) {
	for _, s := range suites {
		fmt.Fprintf(w, "%s  (%s)\n", s.Name, s.Path)
		if s.Description != "" {
			fmt.Fprintf(w, "  %s\n", firstLine(s.Description, 100))
		}
		for _, c := range s.Cases {
			line := fmt.Sprintf("  %-36s %-6s %s", c.ID, c.Mode, i18n.T("evals.render.list_counts", c.Trials, len(c.Checks)))
			if len(c.Tags) > 0 {
				line += "  [" + strings.Join(c.Tags, ", ") + "]"
			}
			if c.Skip != "" {
				line += "  " + i18n.T("evals.render.skipped", firstLine(c.Skip, 60))
			}
			fmt.Fprintln(w, line)
		}
	}
}
