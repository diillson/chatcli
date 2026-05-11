/*
 * ChatCLI - Tests for agent-mode skill assembly
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Exercises the helper that turns the auto-activated skill set into the
 * system-prompt block consumed by AgentMode.Run.
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"go.uber.org/zap"
)

func TestBuildAgentSkillBlocks_NilPersonaHandlerReturnsEmpty(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{}, logger: zap.NewNop()}
	if got := a.buildAgentSkillBlocks("anything", ""); got != "" {
		t.Errorf("nil persona handler must produce empty output; got %q", got)
	}
	if a.skillModelHint != "" || a.skillEffortHint != client.SkillEffort("") {
		t.Errorf("nil persona handler must not mutate hint fields; got (%q, %q)",
			a.skillModelHint, a.skillEffortHint)
	}
}

func TestBuildAgentSkillBlocks_TriggerMatchRendersAutoBlock(t *testing.T) {
	cli, _ := newPipelineCLI(t, map[string]string{
		"alpha": `---
name: alpha
description: trigger on alpha
triggers: ["alpha"]
---
alpha body
`,
	})
	a := &AgentMode{cli: cli, logger: zap.NewNop()}
	out := a.buildAgentSkillBlocks("query mentioning alpha", "")

	if !strings.Contains(out, "# Auto-loaded Skills") {
		t.Errorf("expected auto-loaded header; got:\n%s", out)
	}
	if !strings.Contains(out, "alpha body") {
		t.Errorf("expected the skill body in the rendered block; got:\n%s", out)
	}
}

func TestBuildAgentSkillBlocks_NoMatchProducesEmpty(t *testing.T) {
	cli, _ := newPipelineCLI(t, map[string]string{
		"alpha": `---
name: alpha
description: only fires on a very specific keyword
triggers: ["very-specific-keyword-xyz"]
---
body
`,
	})
	a := &AgentMode{cli: cli, logger: zap.NewNop()}
	if got := a.buildAgentSkillBlocks("unrelated user message", ""); got != "" {
		t.Errorf("no triggers should fire on this input → empty; got %q", got)
	}
}

func TestBuildAgentSkillBlocks_ModelEffortHintsPropagateToAgentMode(t *testing.T) {
	cli, _ := newPipelineCLI(t, map[string]string{
		"hinted": `---
name: hinted
description: carries hints
model: opus
effort: high
triggers: ["hinted"]
---
body
`,
	})
	a := &AgentMode{cli: cli, logger: zap.NewNop()}
	_ = a.buildAgentSkillBlocks("trigger hinted skill", "")
	if a.skillModelHint != "opus" {
		t.Errorf("skillModelHint = %q, want opus", a.skillModelHint)
	}
	if a.skillEffortHint != client.EffortHigh {
		t.Errorf("skillEffortHint = %q, want EffortHigh", a.skillEffortHint)
	}
}
