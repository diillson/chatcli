/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for the task graph. Every attempt at a task is one pattern
 * node hung from the graph's run, so the dashboard shows which task is being
 * tried, how many times, and how each try ended; a task skipped because a
 * dependency failed shows as a blocked point. Only the task's short id, its
 * position and counts travel — never its title, prompt or any output.
 */
package taskgraph

import (
	"context"
	"strconv"

	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseTaskNode is the dashboard node every task attempt aggregates under.
const pulseTaskNode = "taskgraph:task"

// Fixed outcome labels of an attempt.
const (
	pulseOutcomePass       = "pass"
	pulseOutcomeWaived     = "pass (review waived)"
	pulseOutcomeExecError  = "executor error"
	pulseOutcomeGateFail   = "gate fail"
	pulseOutcomeReviewFail = "review fail"
	pulseOutcomeBlocked    = "dependency failed"
)

// pulseTaskIDRunes bounds the agent name carried on a node.
const pulseTaskIDRunes = 24

// pulseAttemptBegin opens the node of one attempt at t under the innermost
// parent in ctx — the graph's run.
func (e *Engine) pulseAttemptBegin(ctx context.Context, t *Task, n int) *pulse.Span {
	if !pulse.Enabled() {
		return nil
	}
	_, span := pulse.BeginCtx(ctx, pulse.KindPattern, pulseTaskNode, "")
	return e.pulseTaskAttrs(span, t).
		With("attempt", strconv.Itoa(n)).
		With("max_attempts", strconv.Itoa(t.MaxAttempts))
}

// pulseTaskAttrs attaches the identity of t that is safe to show: its
// position in the plan, never its id — a plan may name a task with a
// sentence taken from the user's request.
func (e *Engine) pulseTaskAttrs(span *pulse.Span, t *Task) *pulse.Span {
	return span.
		With("index", strconv.Itoa(e.taskIndex(t))).
		With("deps", strconv.Itoa(len(t.Deps))).
		With("agent", pulseClip(t.Agent, pulseTaskIDRunes))
}

// pulseFailStatus is the status of an attempt that did not pass: cancelled
// when the run itself is being stopped, an error otherwise.
func pulseFailStatus(ctx context.Context) string {
	if ctx.Err() != nil {
		return pulse.StatusCancelled
	}
	return pulse.StatusError
}

// pulseBlocked reports a task skipped because a dependency failed.
func (e *Engine) pulseBlocked(parent string, t *Task) {
	if !pulse.Enabled() {
		return
	}
	pulse.Point(pulse.KindPattern, pulseTaskNode, parent, pulse.StatusBlocked, map[string]string{
		"index": strconv.Itoa(e.taskIndex(t)),
		"deps":  strconv.Itoa(len(t.Deps)),
		"state": pulseOutcomeBlocked,
	})
}

// taskIndex is t's position in the plan, -1 when it is not part of it.
func (e *Engine) taskIndex(t *Task) int {
	for i, c := range e.graph.Tasks {
		if c == t {
			return i
		}
	}
	return -1
}

// pulseClip bounds s to n runes.
func pulseClip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
