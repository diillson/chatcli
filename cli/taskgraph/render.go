/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Compact English key=value renderings of graph state. All of it is
 * model-facing (@taskgraph output consumed by the LLM); the /taskgraph
 * command reuses it deliberately so humans and the model read the same
 * ground truth.
 */
package taskgraph

import (
	"fmt"
	"strings"
	"time"
)

// FormatRunReport renders the final (or current) state of a run — the text
// the orchestrator receives when @taskgraph run returns.
func FormatRunReport(g *Graph) string {
	var b strings.Builder
	counts := g.CountByStatus()
	fmt.Fprintf(&b, "--- Task Graph %s (%s) status=%s ---\n", g.RunID, g.Name, g.Status)
	fmt.Fprintf(&b, "tasks=%d done=%d failed=%d blocked=%d", len(g.Tasks), counts[StatusDone], counts[StatusFailed], counts[StatusBlocked])
	if cost := g.TotalCostUSD(); cost > 0 {
		fmt.Fprintf(&b, " cost=$%.4f", cost)
	}
	if !g.StartedAt.IsZero() {
		end := g.EndedAt
		if end.IsZero() {
			end = time.Now()
		}
		fmt.Fprintf(&b, " elapsed=%s", end.Sub(g.StartedAt).Round(time.Second))
	}
	b.WriteString("\n")
	for _, t := range g.Tasks {
		b.WriteString("\n")
		b.WriteString(FormatTaskLine(g, t))
		b.WriteString("\n")
		if n := len(t.Attempts); n > 0 {
			last := t.Attempts[n-1]
			switch t.Status {
			case StatusDone:
				if last.Evidence != "" {
					fmt.Fprintf(&b, "  evidence: %s\n", firstLine(last.Evidence))
				}
			case StatusFailed:
				if last.FailureReason != "" {
					fmt.Fprintf(&b, "  reason: %s\n", firstLine(last.FailureReason))
				}
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// FormatTaskLine renders one task as a single compact line.
func FormatTaskLine(g *Graph, t *Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s status=%s agent=%s", t.ID, t.Title, t.Status, t.Agent)
	if len(t.Deps) > 0 {
		fmt.Fprintf(&b, " deps=%s", strings.Join(t.Deps, ","))
	}
	if n := len(t.Attempts); n > 0 {
		fmt.Fprintf(&b, " attempts=%d/%d", n, t.MaxAttempts)
	}
	if t.CostUSD > 0 {
		fmt.Fprintf(&b, " cost=$%.4f", t.CostUSD)
	}
	if g.RequiresReview(t) {
		if n := len(t.Attempts); n > 0 && t.Attempts[n-1].Verdict != "" {
			fmt.Fprintf(&b, " verdict=%s", t.Attempts[n-1].Verdict)
		}
	} else {
		b.WriteString(" review=waived")
	}
	return b.String()
}

// FormatTaskDetail renders one task in full, attempts included — the
// @taskgraph show output.
func FormatTaskDetail(g *Graph, t *Task) string {
	var b strings.Builder
	b.WriteString(FormatTaskLine(g, t))
	b.WriteString("\n")
	if t.Phase != "" {
		fmt.Fprintf(&b, "phase=%s\n", t.Phase)
	}
	fmt.Fprintf(&b, "prompt: %s\n", t.Prompt)
	if len(t.Validation) > 0 {
		fmt.Fprintf(&b, "validation:\n%s\n", formatContract(t.Validation))
	}
	for _, a := range t.Attempts {
		fmt.Fprintf(&b, "\nattempt %d", a.N)
		if !a.StartedAt.IsZero() && !a.EndedAt.IsZero() {
			fmt.Fprintf(&b, " (%s)", a.EndedAt.Sub(a.StartedAt).Round(time.Second))
		}
		if a.CostUSD > 0 {
			fmt.Fprintf(&b, " cost=$%.4f", a.CostUSD)
		}
		b.WriteString("\n")
		if a.ExecutorRunID != "" {
			fmt.Fprintf(&b, "  executor_run=%s\n", a.ExecutorRunID)
		}
		for _, gr := range a.Gate {
			status := verdictPass
			if !gr.Passed {
				status = verdictFail
			}
			fmt.Fprintf(&b, "  gate $ %s → %s\n", gr.Run, status)
		}
		if a.ReviewerRunID != "" {
			fmt.Fprintf(&b, "  reviewer_run=%s verdict=%s\n", a.ReviewerRunID, a.Verdict)
		}
		if a.Evidence != "" {
			fmt.Fprintf(&b, "  evidence: %s\n", firstLine(a.Evidence))
		}
		if a.FailureReason != "" {
			fmt.Fprintf(&b, "  failure: %s\n", firstLine(a.FailureReason))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// FormatRunList renders ListRuns rows.
func FormatRunList(rows []RunSummary) string {
	if len(rows) == 0 {
		return "no task graph runs yet"
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%s name=%q status=%s tasks=%d done=%d failed=%d created=%s\n",
			r.RunID, r.Name, r.Status, r.Tasks, r.Done, r.Failed, r.CreatedAt.Format("2006-01-02 15:04"))
	}
	return strings.TrimRight(b.String(), "\n")
}

// FormatEvent renders one event for terminal streaming.
func FormatEvent(ev Event) string {
	var b strings.Builder
	b.WriteString(ev.TS.Format("15:04:05"))
	if ev.Task != "" {
		fmt.Fprintf(&b, " [%s]", ev.Task)
	}
	fmt.Fprintf(&b, " %s", ev.Type)
	if ev.Detail != "" {
		fmt.Fprintf(&b, ": %s", ev.Detail)
	}
	return b.String()
}
