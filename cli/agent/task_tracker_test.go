package agent

import (
	"testing"

	"go.uber.org/zap"
)

func TestTaskTracker_ParseReasoning(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := NewTaskTracker(logger)
	tests := []struct {
		name          string
		reasoning     string
		expectedCount int
	}{
		{name: "lista numerada", reasoning: `1. Criar arquivo
2. Rodar teste`, expectedCount: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tracker.ParseReasoning(tt.reasoning)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			plan := tracker.GetPlan()
			if tt.expectedCount == 0 {
				if plan != nil {
					t.Errorf("Expected no plan, got %d tasks", len(plan.Tasks))
				}
				return
			}
			if plan == nil {
				t.Fatal("Expected plan, got nil")
				return
			}
			if len(plan.Tasks) != tt.expectedCount {
				t.Errorf("Expected %d tasks, got %d", tt.expectedCount, len(plan.Tasks))
			}
			tracker.ResetPlan()
		})
	}
}

func TestTaskTracker_MarkCurrentAs(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := NewTaskTracker(logger)
	reasoning := `1. Criar arquivo
2. Rodar teste`
	_ = tracker.ParseReasoning(reasoning)
	plan := tracker.GetPlan()
	if plan == nil {
		t.Fatal("Expected plan, got nil")
		return
	}
	tracker.MarkCurrentAs(TaskInProgress, "")
	if plan.Tasks[0].Status != TaskInProgress {
		t.Error("Expected first task to be in progress")
	}
	tracker.MarkCurrentAs(TaskCompleted, "")
	if plan.Tasks[0].Status != TaskCompleted {
		t.Error("Expected first task to be completed")
	}
	if plan.CurrentTask != 1 {
		t.Errorf("Expected current task to be 1, got %d", plan.CurrentTask)
	}
}

func TestTaskTracker_FailureHandling(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := NewTaskTracker(logger)
	reasoning := `1. Criar arquivo
2. Rodar teste`
	_ = tracker.ParseReasoning(reasoning)
	plan := tracker.GetPlan()
	if plan == nil {
		t.Fatal("Expected plan, got nil")
		return
	}
	for i := 0; i < 3; i++ {
		tracker.MarkCurrentAs(TaskFailed, "erro de teste")
	}
	if !tracker.NeedsReplanning() {
		t.Error("Expected replanning to be needed after 3 failures")
	}
	if plan.FailureCount != 3 {
		t.Errorf("Expected 3 failures, got %d", plan.FailureCount)
	}
}

// TestTaskTracker_SetTasks pins the @todo write semantics: the full
// task list is replaced, statuses honored verbatim, and the
// CurrentTask cursor advances past any prefix of completed tasks.
func TestTaskTracker_SetTasks_FullReplacement(t *testing.T) {
	tracker := NewTaskTracker(zap.NewNop())
	tracker.SetTasks([]TaskSpec{
		{Description: "first", Status: TaskCompleted},
		{Description: "second", Status: TaskCompleted},
		{Description: "third", Status: TaskPending},
	})
	plan := tracker.GetPlan()
	if plan == nil {
		t.Fatal("plan must be created")
	}
	if len(plan.Tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(plan.Tasks))
	}
	if plan.CurrentTask != 2 {
		t.Errorf("CurrentTask must advance past the two completed; got %d", plan.CurrentTask)
	}
	if plan.Tasks[0].Status != TaskCompleted {
		t.Errorf("task[0] status: want completed, got %v", plan.Tasks[0].Status)
	}
}

// TestTaskTracker_SetTasks_ExplicitInProgressFixesCurrent pins the
// non-prefix case: when one task is explicitly in_progress, the
// cursor anchors on its index regardless of prior completion state.
func TestTaskTracker_SetTasks_ExplicitInProgressFixesCurrent(t *testing.T) {
	tracker := NewTaskTracker(zap.NewNop())
	tracker.SetTasks([]TaskSpec{
		{Description: "a", Status: TaskPending},
		{Description: "b", Status: TaskInProgress},
		{Description: "c", Status: TaskCompleted},
	})
	plan := tracker.GetPlan()
	if plan.CurrentTask != 1 {
		t.Errorf("CurrentTask: want 1 (the in_progress task), got %d", plan.CurrentTask)
	}
}

// TestTaskTracker_SetTasks_EmptyStatusDefaultsToPending guards the
// status-coercion path.
func TestTaskTracker_SetTasks_EmptyStatusDefaultsToPending(t *testing.T) {
	tracker := NewTaskTracker(zap.NewNop())
	tracker.SetTasks([]TaskSpec{{Description: "x"}})
	plan := tracker.GetPlan()
	if plan.Tasks[0].Status != TaskPending {
		t.Errorf("empty status must default to pending, got %v", plan.Tasks[0].Status)
	}
}

// TestTaskTracker_MarkByID_HappyPath pins the single-item update.
func TestTaskTracker_MarkByID_HappyPath(t *testing.T) {
	tracker := NewTaskTracker(zap.NewNop())
	tracker.SetTasks([]TaskSpec{
		{Description: "a", Status: TaskPending},
		{Description: "b", Status: TaskPending},
	})
	if !tracker.MarkByID(2, TaskCompleted, "") {
		t.Fatal("MarkByID must return true for valid id")
	}
	plan := tracker.GetPlan()
	if plan.Tasks[1].Status != TaskCompleted {
		t.Errorf("task[1] status: want completed, got %v", plan.Tasks[1].Status)
	}
}

// TestTaskTracker_MarkByID_OutOfRange returns false safely.
func TestTaskTracker_MarkByID_OutOfRange(t *testing.T) {
	tracker := NewTaskTracker(zap.NewNop())
	tracker.SetTasks([]TaskSpec{{Description: "x"}})
	if tracker.MarkByID(99, TaskCompleted, "") {
		t.Error("MarkByID must return false for out-of-range id")
	}
	if tracker.MarkByID(0, TaskCompleted, "") {
		t.Error("MarkByID must return false for id=0 (we are 1-indexed)")
	}
}

// TestTaskTracker_MarkByID_NoPlan returns false safely when no plan
// has been set yet.
func TestTaskTracker_MarkByID_NoPlan(t *testing.T) {
	tracker := NewTaskTracker(zap.NewNop())
	if tracker.MarkByID(1, TaskCompleted, "") {
		t.Error("MarkByID without a plan must return false")
	}
}

// TestTaskTracker_MarkByID_FailedAccumulates pins that three failures
// triggers NeedsReplanning, matching MarkCurrentAs semantics.
func TestTaskTracker_MarkByID_FailedAccumulates(t *testing.T) {
	tracker := NewTaskTracker(zap.NewNop())
	tracker.SetTasks([]TaskSpec{
		{Description: "a"}, {Description: "b"}, {Description: "c"},
	})
	tracker.MarkByID(1, TaskFailed, "e1")
	tracker.MarkByID(2, TaskFailed, "e2")
	tracker.MarkByID(3, TaskFailed, "e3")
	if !tracker.NeedsReplanning() {
		t.Error("three failures must flip NeedsReplanning to true")
	}
}
