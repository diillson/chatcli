package cli

import (
	"strings"
	"testing"
)

// The plan block is sized to the task in every mode prompt: a
// trivial request must not be turned into a three-step plan. The rule
// lives in the stable prompt on purpose — a per-query switch would change
// the system prompt and rewrite the cached prefix.
func TestModePromptsSizePlanToTheTask(t *testing.T) {
	for name, prompt := range map[string]string{
		"CoderSystemPrompt":       CoderSystemPrompt,
		"CoderFormatInstructions": CoderFormatInstructions,
		"AgentFormatInstructions": AgentFormatInstructions,
	} {
		if !strings.Contains(prompt, "sized to the task") {
			t.Errorf("%s must size <plan> to the task", name)
		}
		if !strings.Contains(strings.ToLower(prompt), "single-step") {
			t.Errorf("%s must name the single-step case", name)
		}
	}
	if !strings.Contains(CoderSystemPrompt, "no task list") || !strings.Contains(CoderSystemPrompt, "numbered task list") {
		t.Error("the coder prompt keeps the task list for multi-step work and drops it for single-step work")
	}
}
