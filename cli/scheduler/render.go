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

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/textfold"
)

// padRight pads s with spaces to w runes (rune-aware, unlike %-*s which counts
// bytes and misaligns accented labels like "Ação").
func padRight(s string, w int) string {
	if n := len([]rune(s)); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// StatusBadge returns a fixed-width one-glyph label for a JobStatus.
// Used by the status line prefix and the tree view.
func StatusBadge(s JobStatus) string {
	switch s {
	case StatusPending:
		return " ⏳"
	case StatusBlocked:
		return " ⛓"
	case StatusWaiting:
		return " 👁"
	case StatusRunning:
		return " ▶"
	case StatusPaused:
		return " ❚❚"
	case StatusCompleted:
		return " ✔"
	case StatusFailed:
		return " ✗"
	case StatusCancelled:
		return " ⊘"
	case StatusTimedOut:
		return " ⏱"
	case StatusSkipped:
		return " ↷"
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
		return "  " + i18n.T("scheduler.jobs.empty") + "\n"
	}
	type row struct {
		cols [7]string
	}
	rows := make([]row, 0, len(summaries))
	now := time.Now()
	for _, s := range summaries {
		rows = append(rows, row{cols: [7]string{
			string(s.Status),
			string(s.ID),
			s.Name,
			s.Owner.String(),
			s.Type,
			nextFireLabel(s, now),
			lastLabel(s),
		}})
	}
	header := []string{
		i18n.T("scheduler.render.col_status"),
		i18n.T("scheduler.render.col_id"),
		i18n.T("scheduler.render.col_name"),
		i18n.T("scheduler.render.col_owner"),
		i18n.T("scheduler.render.col_type"),
		i18n.T("scheduler.render.col_next_fire"),
		i18n.T("scheduler.render.col_last"),
	}
	// Each column is as wide as its widest cell, up to a cap: a short
	// table stays short, and a long name is cut instead of pushing the
	// row past the terminal.
	widths := [7]int{}
	caps := [7]int{10, 18, 28, 18, 18, 24, 18}
	for i := range widths {
		widths[i] = len([]rune(header[i]))
		for _, r := range rows {
			if n := len([]rune(r.cols[i])); n > widths[i] {
				widths[i] = n
			}
		}
		if widths[i] > caps[i] {
			widths[i] = caps[i]
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  %s\n", rowLine(widths, header))
	fmt.Fprintf(&b, "  %s\n", rowLineSep(widths))
	for _, r := range rows {
		fmt.Fprintf(&b, "  %s\n", rowLine(widths, r.cols[:]))
	}
	return b.String()
}

// nextFireLabel says what the job is due to do next. The fire time alone
// misleads once a job has fired: a job polling its condition, or running
// its action, has a fire time in the past and is not overdue, and a
// finished job is not due at all. "Overdue" is kept for a job that is
// still queued after its time.
func nextFireLabel(s JobSummary, now time.Time) string {
	switch {
	case s.Status.IsTerminal(), s.Status == StatusPaused:
		return "—"
	case s.Status == StatusWaiting:
		if s.WaitDeadline.IsZero() {
			return i18n.T("scheduler.render.polling")
		}
		left := s.WaitDeadline.Sub(now)
		if left < 0 {
			left = 0
		}
		return i18n.T("scheduler.render.polling_left", compactDuration(left))
	case s.Status == StatusRunning:
		return i18n.T("scheduler.render.running_now")
	case s.Status == StatusBlocked:
		return i18n.T("scheduler.render.after_deps")
	case s.NextFireAt.IsZero():
		return "—"
	}
	d := s.NextFireAt.Sub(now)
	switch {
	case d >= 0:
		return i18n.T("scheduler.render.in", compactDuration(d))
	case d > -overdueGrace:
		return i18n.T("scheduler.render.due_now")
	}
	return i18n.T("scheduler.render.overdue")
}

// overdueGrace is how long past its fire time a queued job is simply
// "due": the pump picks it up within moments, and calling that overdue
// would flag every job caught between its fire time and its worker.
const overdueGrace = 5 * time.Second

// lastLabel says how the latest execution went. For a job gated on a
// condition the latest execution is a check of that condition, whose raw
// outcome ("skipped" when not yet met, "failed" when the probe errored,
// as a service still starting does) reads as a verdict on the job. It is
// shown as what it is, with the number of checks so far.
func lastLabel(s JobSummary) string {
	if s.LastOutcome == "" {
		return "—"
	}
	gated := strings.HasSuffix(s.Type, "+wait")
	if gated && (s.Status == StatusWaiting || s.Status == StatusTimedOut) {
		key := ""
		switch s.LastOutcome {
		case OutcomeSkipped:
			key = "scheduler.render.cond_unmet"
		case OutcomeFailed:
			key = "scheduler.render.cond_error"
		}
		if key != "" {
			if s.WaitPolls > 0 {
				return fmt.Sprintf("%s ×%d", i18n.T(key), s.WaitPolls)
			}
			return i18n.T(key)
		}
	}
	return string(s.LastOutcome)
}

func rowLine(widths [7]int, cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		w := widths[i]
		if len([]rune(c)) > w {
			c = string([]rune(c)[:w-1]) + "…"
		}
		parts[i] = padRight(c, w)
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

// kv is one labeled row of the /jobs show view.
type kv struct{ label, value string }

// waitShowRows are the rows describing a job's wait: what it waits for,
// its budget and, while it is waiting, how far along it is.
func waitShowRows(j *Job) []kv {
	if j.Wait == nil {
		return nil
	}
	var rows []kv
	rows = append(rows, kv{i18n.T("scheduler.render.wait"), DescribeCondition(j.Wait.Condition)})
	budget := i18n.T("scheduler.render.wait_budget_value", compactDuration(j.Budget.PollInterval), compactDuration(j.Budget.WaitTimeout))
	if j.Budget.MaxPolls > 0 {
		budget += i18n.T("scheduler.render.wait_budget_max_polls", j.Budget.MaxPolls)
	}
	rows = append(rows, kv{i18n.T("scheduler.render.wait_budget"), budget})
	if j.Status == StatusWaiting {
		polls, deadline, detail := j.waitProgressLocked()
		progress := i18n.T("scheduler.render.wait_progress_value", polls)
		if !deadline.IsZero() {
			left := time.Until(deadline)
			if left < 0 {
				left = 0
			}
			progress += i18n.T("scheduler.render.wait_progress_left", compactDuration(left))
		}
		rows = append(rows, kv{i18n.T("scheduler.render.wait_progress"), progress})
		if detail != "" {
			rows = append(rows, kv{i18n.T("scheduler.render.wait_last_check"), detail})
		}
	}
	return rows
}

// RenderShow emits the detailed `/jobs show <id>` view.
func RenderShow(j *Job) string {
	if j == nil {
		return i18n.T("scheduler.jobs.empty") + "\n"
	}

	// Build the labeled rows, then align the colons to the widest (translated)
	// label so the column lines up in any language.
	rows := []kv{
		{i18n.T("scheduler.render.owner"), j.Owner.String()},
		{i18n.T("scheduler.render.status"), fmt.Sprintf("%s %s", j.Status, StatusBadge(j.Status))},
		{i18n.T("scheduler.render.type"), string(j.Schedule.Kind)},
		{i18n.T("scheduler.render.created"), j.CreatedAt.Format(time.RFC3339)},
	}
	// The fire time only says something while the job is still queued.
	if !j.NextFireAt.IsZero() && (j.Status == StatusPending || j.Status == StatusBlocked || j.Status == StatusPaused) {
		rows = append(rows, kv{i18n.T("scheduler.render.next_fire"), fmt.Sprintf("%s (%s)", j.NextFireAt.Format(time.RFC3339), compactDuration(time.Until(j.NextFireAt)))})
	}
	if !j.FinishedAt.IsZero() {
		rows = append(rows, kv{i18n.T("scheduler.render.finished"), j.FinishedAt.Format(time.RFC3339)})
	}
	rows = append(rows, kv{i18n.T("scheduler.render.attempts"), fmt.Sprintf("%d", j.Attempts)})
	rows = append(rows, kv{i18n.T("scheduler.render.action"), string(j.Action.Type)})
	if cmd, ok := j.Action.Payload["command"].(string); ok {
		rows = append(rows, kv{i18n.T("scheduler.render.command"), cmd})
	}
	rows = append(rows, waitShowRows(j)...)
	if len(j.DependsOn) > 0 {
		rows = append(rows, kv{i18n.T("scheduler.render.depends_on"), fmt.Sprintf("%v", j.DependsOn)})
	}
	if len(j.Triggers) > 0 {
		rows = append(rows, kv{i18n.T("scheduler.render.triggers"), fmt.Sprintf("%v", j.Triggers)})
	}
	if j.Description != "" {
		rows = append(rows, kv{i18n.T("scheduler.render.description"), j.Description})
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
		rows = append(rows, kv{i18n.T("scheduler.render.tags"), strings.Join(tags, ", ")})
	}

	labelW := 0
	for _, r := range rows {
		if n := len([]rune(r.label)); n > labelW {
			labelW = n
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Job %s (%s)\n", j.Name, j.ID)
	for _, r := range rows {
		fmt.Fprintf(&b, "  %s : %s\n", padRight(r.label, labelW), r.value)
	}

	if n := len(j.History); n > 0 {
		header := "  " + i18n.T("scheduler.render.history", n)
		if j.CycleCount > 0 {
			header += i18n.T("scheduler.render.cycles", j.CycleCount)
		}
		header += "):\n" // the history key opens the parenthesis
		fmt.Fprint(&b, header)
		for i := len(j.History) - 1; i >= 0 && i >= len(j.History)-10; i-- {
			r := j.History[i]
			// For recurring jobs, render "#cycle.attempt"; for one-shot
			// jobs keep the existing "#attempt" form unchanged.
			tag := fmt.Sprintf("#%d", r.AttemptNum)
			if r.CycleNum > 0 {
				tag = fmt.Sprintf("#%d.%d", r.CycleNum, r.AttemptNum)
			}
			fmt.Fprintf(&b, "    %s %s %s (%s)", tag, r.StartedAt.Format(time.RFC3339), r.Outcome, compactDuration(r.Duration))
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
		fmt.Fprintf(&b, "  %s:\n", i18n.T("scheduler.render.transitions"))
		for _, t := range j.Transitions {
			fmt.Fprintf(&b, "    %s  %s → %s  %s\n", t.At.Format(time.RFC3339), t.From, t.To, t.Message)
		}
	}
	return b.String()
}

// DescribeCondition renders a condition as the target a person recognizes
// ("http_status http://host/health", "shell_exit pg_isready") instead of
// the bare evaluator type, which says nothing about what is being waited
// for when several waits are listed side by side.
func DescribeCondition(c Condition) string {
	var b strings.Builder
	if c.Negate {
		b.WriteString("not ")
	}
	b.WriteString(c.Type)
	if len(c.Children) > 0 {
		parts := make([]string, 0, len(c.Children))
		for _, ch := range c.Children {
			parts = append(parts, DescribeCondition(ch))
		}
		b.WriteString("(" + strings.Join(parts, ", ") + ")")
		return b.String()
	}
	target := ""
	for _, k := range []string{"url", "cmd", "path", "container", "prompt"} {
		if v, ok := c.Spec[k].(string); ok && v != "" {
			target = v
			break
		}
	}
	switch {
	case target != "":
	case c.Spec["host"] != nil:
		target = fmt.Sprintf("%v:%v", c.Spec["host"], c.Spec["port"])
	case c.Spec["name"] != nil:
		target = fmt.Sprintf("%v/%v", c.Spec["kind"], c.Spec["name"])
		if ns, ok := c.Spec["namespace"].(string); ok && ns != "" {
			target = fmt.Sprintf("%v/%s/%v", c.Spec["kind"], ns, c.Spec["name"])
		}
	}
	if target != "" {
		b.WriteString(" " + truncRender(target, 80))
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
	return textfold.Cut(s, n) + "…"
}
