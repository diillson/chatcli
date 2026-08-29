/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Learning digest: at the end of a @taskgraph run, feed what the graph
 * discovered back into the session's long-term memory — the same pipeline
 * that learns from normal turns. A run's verdicts, evidence and retries are
 * exactly the material the memory extractor (facts + episodes + topics) and
 * self-evolution (skill candidates) need; without this, everything the
 * graph learned evaporated with the report.
 *
 * Two sinks, both best-effort (nothing here can fail or block a run):
 *   1. a compact digest segment → memWorker.nudgeSegment (facts/episodes/
 *      self-evolve, on the normal cadence via the durable WAL);
 *   2. one Reflexion lesson per FAILED task → the lesson queue.
 */
package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/cli/taskgraph"
	"github.com/diillson/chatcli/models"
)

// taskGraphDigestMaxChars bounds each digest message: the memory extractor
// truncates the middle of any message past this, so the digest must fit.
const taskGraphDigestMaxChars = 1400

// queueLearningDigest enqueues a memory segment for the finished run and a
// Reflexion lesson per failed task. Best-effort throughout.
func (a *taskGraphAdapter) queueLearningDigest(ctx context.Context, g *taskgraph.Graph) {
	if a.cli == nil || g == nil {
		return
	}
	if a.cli.memWorker != nil {
		digest := formatTaskGraphDigest(g)
		a.cli.memWorker.nudgeSegment(ctx, []models.Message{
			{Role: "user", Content: fmt.Sprintf("I orchestrated the @taskgraph run %s (%s) — record what it delivered and what to remember.", g.RunID, g.Name)},
			{Role: "assistant", Content: digest},
		})
	}
	a.queueFailedTaskLessons(ctx, g)
}

// formatTaskGraphDigest condenses the final graph into a <taskGraphDigestMaxChars
// summary: run outcome, then one line per task with verdict and the first
// line of evidence or failure reason.
func formatTaskGraphDigest(g *taskgraph.Graph) string {
	var b strings.Builder
	counts := g.CountByStatus()
	fmt.Fprintf(&b, "Task graph %q finished: status=%s, %d tasks (%d done, %d failed/blocked)",
		g.Name, g.Status, len(g.Tasks), counts[taskgraph.StatusDone], counts[taskgraph.StatusFailed]+counts[taskgraph.StatusBlocked])
	if cost := g.TotalCostUSD(); cost > 0 {
		fmt.Fprintf(&b, ", cost $%.4f", cost)
	}
	b.WriteString(".\n")
	for _, t := range g.Tasks {
		line := fmt.Sprintf("- [%s] %s: %s", t.ID, firstLineTrim(t.Title, 60), t.Status)
		if note := lastAttemptNote(t); note != "" {
			line += " — " + firstLineTrim(note, 120)
		}
		if b.Len()+len(line) > taskGraphDigestMaxChars {
			b.WriteString("- …(truncated)\n")
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// queueFailedTaskLessons enqueues one Reflexion lesson per failed task, so
// the "what went wrong / do differently" knowledge is generalized. Gated by
// the session's reflexion config (the enqueuer is nil when disabled).
func (a *taskGraphAdapter) queueFailedTaskLessons(ctx context.Context, g *taskgraph.Graph) {
	if a.cli.agentMode == nil {
		return
	}
	enqueuer := a.cli.reflexionEnqueuer(ctx, a.cli.agentMode.qualityConfig.Reflexion.Queue)
	if enqueuer == nil {
		return
	}
	for _, t := range g.Tasks {
		if t.Status != taskgraph.StatusFailed {
			continue
		}
		var last taskgraph.Attempt
		if n := len(t.Attempts); n > 0 {
			last = t.Attempts[n-1]
		}
		outcome := strings.TrimSpace(last.FailureReason + " " + last.Evidence)
		if outcome == "" {
			outcome = "task failed without a recorded reason"
		}
		_ = enqueuer.Enqueue(ctx, quality.LessonRequest{
			Task:    t.Title,
			Attempt: firstLineTrim(last.ExecutorOutput, 800),
			Outcome: firstLineTrim(outcome, 400),
			Trigger: "error",
		})
	}
}

// firstLineTrim returns the first non-empty line of s, capped at n runes.
func firstLineTrim(s string, n int) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			if r := []rune(t); len(r) > n {
				return string(r[:n]) + "…"
			}
			return t
		}
	}
	return ""
}

// lastAttemptNote returns the most informative note of a task's last attempt.
func lastAttemptNote(t *taskgraph.Task) string {
	n := len(t.Attempts)
	if n == 0 {
		return ""
	}
	last := t.Attempts[n-1]
	if last.Evidence != "" {
		return last.Evidence
	}
	return last.FailureReason
}
