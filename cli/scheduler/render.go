/*
 * ChatCLI - Scheduler: terminal render helpers for /jobs output.
 *
 * These helpers are string generators — the CLI package decides where
 * to print them. Keeping render in the scheduler package lets the
 * daemon IPC client reuse the same formatting when the operator
 * attaches remotely.
 *
 * Formatting is chosen for terminal consumption: ASCII boxes + ANSI-
 * agnostic (no colors here; color is applied by the caller via the
 * existing cli/colors.go palette).
 */
package scheduler

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// StatusBadge returns a fixed-width one-glyph label for a JobStatus.
// Used by the status line prefix and the tree view.
func StatusBadge(s JobStatus) string {
	switch s {
	case StatusPending:
		return "⏳"
	case StatusBlocked:
		return "⛓"
	case StatusWaiting:
		return "👁"
	case StatusRunning:
		return "▶"
	case StatusPaused:
		return "❚❚"
	case StatusCompleted:
		return "✔"
	case StatusFailed:
		return "✗"
	case StatusCancelled:
		return "⊘"
	case StatusTimedOut:
		return "⏱"
	case StatusSkipped:
		return "↷"
	}
	return "?"
}

// StatusLine renders a short indicator for the prompt prefix:
// "[jobs: 2▶ 3⏳ 1⏱]"
func StatusLine(summaries []JobSummary) string {
	if len(summaries) == 0 {
		return ""
	}
	counts := make(map[JobStatus]int)
	for _, s := range summaries {
		counts[s.Status]++
	}
	if counts[StatusRunning] == 0 &&
		counts[StatusPending] == 0 &&
		counts[StatusWaiting] == 0 &&
		counts[StatusBlocked] == 0 {
		return ""
	}
	parts := []string{}
	if n := counts[StatusRunning]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d%s", n, StatusBadge(StatusRunning)))
	}
	if n := counts[StatusWaiting]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d%s", n, StatusBadge(StatusWaiting)))
	}
	if n := counts[StatusPending]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d%s", n, StatusBadge(StatusPending)))
	}
	if n := counts[StatusBlocked]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d%s", n, StatusBadge(StatusBlocked)))
	}
	if n := counts[StatusFailed]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d%s", n, StatusBadge(StatusFailed)))
	}
	return "[jobs: " + strings.Join(parts, " ") + "]"
}

// RenderList emits the /jobs list table as plain text (no color).
// Columns: STATUS | ID | NAME | OWNER | TYPE | NEXT FIRE | LAST
func RenderList(summaries []JobSummary) string {
	if len(summaries) == 0 {
		return "  (no jobs)\n"
	}
	type row struct {
		cols [7]string
	}
	rows := make([]row, 0, len(summaries))
	now := time.Now()
	for _, s := range summaries {
		nextFire := ""
		if !s.NextFireAt.IsZero() {
			d := s.NextFireAt.Sub(now)
			if d < 0 {
				nextFire = "overdue"
			} else {
				nextFire = "in " + compactDuration(d)
			}
		}
		lastCol := "—"
		if s.LastOutcome != "" {
			lastCol = string(s.LastOutcome)
		}
		rows = append(rows, row{cols: [7]string{
			string(s.Status),
			string(s.ID),
			s.Name,
			s.Owner.String(),
			s.Type,
			nextFire,
			lastCol,
		}})
	}
	widths := [7]int{8, 18, 22, 18, 18, 14, 10}
	header := []string{"STATUS", "ID", "NAME", "OWNER", "TYPE", "NEXT FIRE", "LAST"}
	var b strings.Builder
	fmt.Fprintf(&b, "  %s\n", rowLine(widths, header))
	fmt.Fprintf(&b, "  %s\n", rowLineSep(widths))
	for _, r := range rows {
		fmt.Fprintf(&b, "  %s\n", rowLine(widths, r.cols[:]))
	}
	return b.String()
}

func rowLine(widths [7]int, cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		w := widths[i]
		if len([]rune(c)) > w {
			c = string([]rune(c)[:w-1]) + "…"
		}
		parts[i] = fmt.Sprintf("%-*s", w, c)
	}
	return strings.Join(parts, "  ")
}

func rowLineSep(widths [7]int) string {
	parts := make([]string, 7)
	for i, w := range widths {
		parts[i] = strings.Repeat("─", w)
	}
	return strings.Join(parts, "  ")
}

// RenderShow emits the detailed `/jobs show <id>` view.
func RenderShow(j *Job) string {
	if j == nil {
		return "(no job)\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Job %s (%s)\n", j.Name, j.ID)
	fmt.Fprintf(&b, "  Owner    : %s\n", j.Owner)
	fmt.Fprintf(&b, "  Status   : %s %s\n", j.Status, StatusBadge(j.Status))
	fmt.Fprintf(&b, "  Type     : %s\n", j.Schedule.Kind)
	fmt.Fprintf(&b, "  Created  : %s\n", j.CreatedAt.Format(time.RFC3339))
	if !j.NextFireAt.IsZero() {
		fmt.Fprintf(&b, "  NextFire : %s (%s)\n", j.NextFireAt.Format(time.RFC3339), compactDuration(time.Until(j.NextFireAt)))
	}
	if !j.FinishedAt.IsZero() {
		fmt.Fprintf(&b, "  Finished : %s\n", j.FinishedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "  Attempts : %d\n", j.Attempts)
	fmt.Fprintf(&b, "  Action   : %s\n", j.Action.Type)
	if cmd, ok := j.Action.Payload["command"].(string); ok {
		fmt.Fprintf(&b, "    command: %s\n", cmd)
	}
	if j.Wait != nil {
		fmt.Fprintf(&b, "  Wait     : %s\n", j.Wait.Condition.Type)
	}
	if len(j.DependsOn) > 0 {
		fmt.Fprintf(&b, "  DependsOn: %v\n", j.DependsOn)
	}
	if len(j.Triggers) > 0 {
		fmt.Fprintf(&b, "  Triggers : %v\n", j.Triggers)
	}
	if j.Description != "" {
		fmt.Fprintf(&b, "  Description: %s\n", j.Description)
	}
	if len(j.Tags) > 0 {
		tags := make([]string, 0, len(j.Tags))
		for k, v := range j.Tags {
			if v == "" {
				tags = append(tags, k)
			} else {
				tags = append(tags, fmt.Sprintf("%s=%s", k, v))
			}
		}
		sort.Strings(tags)
		fmt.Fprintf(&b, "  Tags     : %s\n", strings.Join(tags, ", "))
	}
	if n := len(j.History); n > 0 {
		fmt.Fprintf(&b, "  History (%d):\n", n)
		for i := len(j.History) - 1; i >= 0 && i >= len(j.History)-10; i-- {
			r := j.History[i]
			fmt.Fprintf(&b, "    #%d %s %s (%s)", r.AttemptNum, r.StartedAt.Format(time.RFC3339), r.Outcome, compactDuration(r.Duration))
			if r.Error != "" {
				fmt.Fprintf(&b, "  err=%s", truncRender(r.Error, 80))
			}
			if r.ConditionDetails != "" {
				fmt.Fprintf(&b, "  cond=%s", truncRender(r.ConditionDetails, 80))
			}
			fmt.Fprintln(&b)
		}
	}
	if n := len(j.Transitions); n > 0 && n <= 16 {
		fmt.Fprintf(&b, "  Transitions:\n")
		for _, t := range j.Transitions {
			fmt.Fprintf(&b, "    %s  %s → %s  %s\n", t.At.Format(time.RFC3339), t.From, t.To, t.Message)
		}
	}
	return b.String()
}

// RenderTree emits the DAG view rooted at any top-level job (one whose
// ParentID is empty or whose parent isn't in the list).
func RenderTree(jobs []*Job) string {
	byID := map[JobID]*Job{}
	for _, j := range jobs {
		byID[j.ID] = j
	}
	parentOf := map[JobID]JobID{}
	for _, j := range jobs {
		if j.ParentID != "" {
			parentOf[j.ID] = j.ParentID
		}
		for _, t := range j.Triggers {
			parentOf[t] = j.ID
		}
		for _, d := range j.DependsOn {
			parentOf[j.ID] = d
		}
	}
	// Roots = jobs with no parent in the set.
	roots := []*Job{}
	for _, j := range jobs {
		if _, ok := parentOf[j.ID]; !ok {
			roots = append(roots, j)
		}
	}
	sort.Slice(roots, func(i, k int) bool { return roots[i].CreatedAt.Before(roots[k].CreatedAt) })

	var b strings.Builder
	var walk func(prefix string, j *Job, isLast bool)
	walk = func(prefix string, j *Job, isLast bool) {
		branch := "├─"
		nextPrefix := prefix + "│ "
		if isLast {
			branch = "└─"
			nextPrefix = prefix + "  "
		}
		fmt.Fprintf(&b, "%s%s %s %s (%s)\n", prefix, branch, StatusBadge(j.Status), j.Name, j.ID)
		// Children = Triggers + DependsOn-reverse for this id.
		children := []*Job{}
		for _, tid := range j.Triggers {
			if c, ok := byID[tid]; ok {
				children = append(children, c)
			}
		}
		for _, other := range jobs {
			for _, d := range other.DependsOn {
				if d == j.ID {
					children = append(children, other)
				}
			}
		}
		// Dedupe.
		seen := map[JobID]bool{}
		uniq := children[:0]
		for _, c := range children {
			if !seen[c.ID] {
				uniq = append(uniq, c)
				seen[c.ID] = true
			}
		}
		sort.Slice(uniq, func(i, k int) bool { return uniq[i].CreatedAt.Before(uniq[k].CreatedAt) })
		for i, c := range uniq {
			walk(nextPrefix, c, i == len(uniq)-1)
		}
	}
	for i, r := range roots {
		walk("", r, i == len(roots)-1)
	}
	return b.String()
}

// compactDuration formats a duration as "2m3s" / "1h4m" / "45s".
func compactDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d/time.Millisecond)
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) - m*60
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) - h*60
	return fmt.Sprintf("%dh%dm", h, m)
}

func truncRender(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
