/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

// A blocked task keeps its reason, moves the focus to the next workable
// task and never counts as a failure.
func TestTaskTracker_MarkByID_BlockedMovesFocusKeepsReason(t *testing.T) {
	tr := NewTaskTracker(zap.NewNop())
	tr.SetTasks([]TaskSpec{{Description: "ask for creds"}, {Description: "deploy"}, {Description: "verify"}})

	if !tr.MarkByID(1, TaskBlocked, "waiting for the staging credentials") {
		t.Fatal("MarkByID blocked must succeed")
	}
	plan := tr.GetPlan()
	if plan.CurrentTask != 1 {
		t.Fatalf("focus must move to the next workable task (1), got %d", plan.CurrentTask)
	}
	if plan.FailureCount != 0 || plan.NeedsReplan {
		t.Fatalf("blocked is not a failure: count=%d replan=%v", plan.FailureCount, plan.NeedsReplan)
	}
	if got := plan.Tasks[0].Error; got != "waiting for the staging credentials" {
		t.Fatalf("reason not kept: %q", got)
	}
	out := tr.FormatProgress()
	for _, want := range []string{"[~] 1. ask for creds", "Blocked: waiting for the staging credentials", "0/3 done, 1 blocked"} {
		if !strings.Contains(out, want) {
			t.Fatalf("progress lacks %q:\n%s", want, out)
		}
	}

	// Resuming the task (in_progress) brings the focus back and clears the reason.
	if !tr.MarkByID(1, TaskInProgress, "") {
		t.Fatal("MarkByID in_progress must succeed")
	}
	plan = tr.GetPlan()
	if plan.CurrentTask != 0 || plan.Tasks[0].Error != "" {
		t.Fatalf("resume: focus=%d reason=%q", plan.CurrentTask, plan.Tasks[0].Error)
	}
}

// A cancelled task is terminal: it leaves the progress denominator and the
// focus skips over it, so a plan whose rest is cancelled reads as done.
func TestTaskTracker_CancelledLeavesDenominatorAndFocusSkips(t *testing.T) {
	tr := NewTaskTracker(zap.NewNop())
	tr.SetTasks([]TaskSpec{{Description: "a"}, {Description: "b"}, {Description: "c"}})

	if !tr.MarkByID(2, TaskCancelled, "superseded by a") {
		t.Fatal("cancel must succeed")
	}
	tr.MarkByID(1, TaskCompleted, "")
	plan := tr.GetPlan()
	if plan.CurrentTask != 2 {
		t.Fatalf("focus must skip the cancelled task 2 and land on 3 (index 2), got %d", plan.CurrentTask)
	}
	out := tr.FormatProgress()
	for _, want := range []string{"[-] 2. b", "Cancelled: superseded by a", "1/2 done, 1 cancelled"} {
		if !strings.Contains(out, want) {
			t.Fatalf("progress lacks %q:\n%s", want, out)
		}
	}
	tr.MarkByID(3, TaskCancelled, "")
	if plan = tr.GetPlan(); plan.CurrentTask != 3 {
		t.Fatalf("no workable task left: focus must be past the end, got %d", plan.CurrentTask)
	}
	if out := tr.FormatProgress(); !strings.Contains(out, "1/1 done, 2 cancelled") {
		t.Fatalf("progress: %s", out)
	}
}

// SetTasks and AppendTasks keep the reason of failed/blocked/cancelled
// specs and start the focus on the first workable task.
func TestTaskTracker_SetTasks_ReasonsAndFocusSkipNonWorkable(t *testing.T) {
	tr := NewTaskTracker(zap.NewNop())
	tr.SetTasks([]TaskSpec{
		{Description: "done", Status: TaskCompleted},
		{Description: "dropped", Status: TaskCancelled, Reason: "not needed"},
		{Description: "stuck", Status: TaskBlocked, Reason: "needs approval"},
		{Description: "next"},
	})
	plan := tr.GetPlan()
	if plan.CurrentTask != 3 {
		t.Fatalf("focus must land on the first workable task (index 3), got %d", plan.CurrentTask)
	}
	if plan.Tasks[1].Error != "not needed" || plan.Tasks[2].Error != "needs approval" {
		t.Fatalf("reasons not kept: %q / %q", plan.Tasks[1].Error, plan.Tasks[2].Error)
	}
	ids := tr.AppendTasks([]TaskSpec{{Description: "later", Status: TaskBlocked, Reason: "after release"}})
	if len(ids) != 1 || tr.GetPlan().Tasks[4].Error != "after release" {
		t.Fatalf("append: ids=%v reason=%q", ids, tr.GetPlan().Tasks[4].Error)
	}
}

func TestTaskStatus_ValidAndWorkable(t *testing.T) {
	for _, s := range []TaskStatus{TaskPending, TaskInProgress, TaskCompleted, TaskFailed, TaskBlocked, TaskCancelled} {
		if !s.Valid() {
			t.Fatalf("%s must be valid", s)
		}
	}
	if TaskStatus("done").Valid() || TaskStatus("").Valid() {
		t.Fatal("unknown/empty statuses are not valid")
	}
	for s, want := range map[TaskStatus]bool{TaskPending: true, TaskInProgress: true, TaskFailed: true, TaskCompleted: false, TaskBlocked: false, TaskCancelled: false} {
		if s.Workable() != want {
			t.Fatalf("%s workable=%v want %v", s, s.Workable(), want)
		}
	}
}
