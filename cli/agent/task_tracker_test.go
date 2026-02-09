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
