package scheduler

import (
	"testing"

	"go.uber.org/zap"
)

func TestCanTransition_Table(t *testing.T) {
	// Pending → Running allowed, Pending → Completed forbidden.
	if !canTransition(StatusPending, StatusRunning) {
		t.Error("pending→running should be allowed")
	}
	if canTransition(StatusPending, StatusCompleted) {
		t.Error("pending→completed must be forbidden")
	}
	// Terminal cannot re-enter any state.
	for _, term := range []JobStatus{StatusCompleted, StatusFailed, StatusCancelled, StatusTimedOut, StatusSkipped} {
		for _, dst := range []JobStatus{StatusPending, StatusRunning, StatusWaiting} {
			if canTransition(term, dst) {
				t.Errorf("%s→%s must be forbidden", term, dst)
			}
		}
	}
}

func TestJob_Transition_RecordsReason(t *testing.T) {
	j := &Job{Status: StatusPending}
	if err := j.transition(StatusRunning, "start", zap.NewNop()); err != nil {
		t.Fatalf("first transition: %v", err)
	}
	if j.Status != StatusRunning {
		t.Errorf("status after transition: %s", j.Status)
	}
	if len(j.Transitions) != 1 || j.Transitions[0].Message != "start" {
		t.Errorf("transitions not recorded: %+v", j.Transitions)
	}
	// Illegal: pending → pending (already left pending).
	if err := j.transition(StatusPending, "should fail", zap.NewNop()); err == nil {
		t.Error("expected error on illegal transition")
	}
}

func TestJob_RecordExecution_RingBuffer(t *testing.T) {
	j := &Job{HistoryLimit: 3}
	for i := 0; i < 5; i++ {
		j.recordExecution(ExecutionResult{AttemptNum: i + 1})
	}
	if len(j.History) != 3 {
		t.Fatalf("history len: %d want 3", len(j.History))
	}
	// Should have attempts 3, 4, 5.
	if j.History[0].AttemptNum != 3 || j.History[2].AttemptNum != 5 {
		t.Errorf("history contents: %+v", j.History)
	}
	if j.LastResult == nil || j.LastResult.AttemptNum != 5 {
		t.Error("last result wrong")
	}
}
