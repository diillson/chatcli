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

// TestFormatProgress_EnglishBlock pins the progress block, which is shown
// in the plan card and returned to the model by the todo tool.
func TestFormatProgress_EnglishBlock(t *testing.T) {
	tr := NewTaskTracker(zap.NewNop())
	if tr.FormatProgress() != "" {
		t.Fatal("no plan must render nothing")
	}
	tr.SetTasks([]TaskSpec{{Description: "read"}, {Description: "patch"}})
	tr.MarkCurrentAs(TaskCompleted, "")
	for i := 0; i < 3; i++ {
		tr.MarkCurrentAs(TaskFailed, "boom")
	}
	got := tr.FormatProgress()
	for _, w := range []string{"Action plan:", "[x] 1. read", "[!] 2. patch", "Error: boom", "Progress: 1/2 done, 1 failed", "WARNING: multiple failures detected"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in %q", w, got)
		}
	}
}
