/*
 * ChatCLI - Tests for agent-mode skill assembly
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Exercises the helpers that turn the pinned/auto-activated skill sets into
 * the system-prompt block consumed by AgentMode.Run. The semantic invariants
 * tested here:
 *
 *   - pinned-skills block must precede the auto-loaded block (so they win
 *     ties under pickSkillModelAndEffort's "first non-empty wins" rule);
 *   - the two blocks are separated by a blank line so the LLM can tell them
 *     apart;
 *   - empty pinned or auto sets must still render the other side cleanly;
 *   - model/effort frontmatter hints from any matched skill must surface
 *     on the AgentMode struct so the orchestrator can route the turn.
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/pkg/persona"
	"go.uber.org/zap"
)

func TestConcatSkillBlocks_PinnedBeforeAutoWithBlankLineSeparator(t *testing.T) {
	pinned := []*persona.Skill{
		{Name: "pinA", Description: "pinned", Content: "pinned-body"},
	}
	auto := []*persona.Skill{
		{Name: "autoB", Description: "auto", Content: "auto-body"},
	}
	out := concatSkillBlocks(pinned, auto)

	pinnedAt := strings.Index(out, "# Pinned Skills")
	autoAt := strings.Index(out, "# Auto-loaded Skills")
	if pinnedAt < 0 || autoAt < 0 {
		t.Fatalf("missing one of the headers; pinnedAt=%d autoAt=%d\nout:\n%s",
			pinnedAt, autoAt, out)
	}
	if pinnedAt >= autoAt {
		t.Fatalf("pinned must come first; pinnedAt=%d autoAt=%d", pinnedAt, autoAt)
	}
	// Between the last char of the pinned block body and the auto header
	// we must find exactly the "\n\n" separator the function appends.
	if !strings.Contains(out, "\n\n# Auto-loaded Skills") {
		t.Errorf("missing blank-line separator between blocks\nout:\n%s", out)
	}
}

func TestConcatSkillBlocks_EmptyInputs(t *testing.T) {
	if concatSkillBlocks(nil, nil) != "" {
		t.Error("both nil → expected empty string")
	}
	pinnedOnly := concatSkillBlocks(
		[]*persona.Skill{{Name: "p", Content: "body"}}, nil,
	)
	if !strings.Contains(pinnedOnly, "# Pinned Skills") {
		t.Error("pinned-only output missing the pinned header")
	}
	if strings.Contains(pinnedOnly, "# Auto-loaded Skills") {
		t.Error("pinned-only output unexpectedly includes the auto header")
	}
	autoOnly := concatSkillBlocks(
		nil, []*persona.Skill{{Name: "a", Content: "body"}},
	)
	if !strings.Contains(autoOnly, "# Auto-loaded Skills") {
		t.Error("auto-only output missing the auto header")
	}
	if strings.Contains(autoOnly, "# Pinned Skills") {
		t.Error("auto-only output unexpectedly includes the pinned header")
	}
}

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

func TestBuildAgentSkillBlocks_PinnedAndTriggerMatchBothFire(t *testing.T) {
	cli, _ := newPipelineCLI(t, map[string]string{
		"alpha": `---
name: alpha
description: trigger on alpha
triggers: ["alpha"]
---
alpha body
`,
		"bravo": `---
name: bravo
description: pinned skill
---
bravo body
`,
	})
	cli.skillHandler.Pin("bravo")

	a := &AgentMode{cli: cli, logger: zap.NewNop()}
	out := a.buildAgentSkillBlocks("query mentioning alpha", "")

	for _, want := range []string{
		"# Pinned Skills", "bravo body",
		"# Auto-loaded Skills", "alpha body",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected to find %q in output; full output:\n%s", want, out)
		}
	}
	if strings.Index(out, "# Pinned Skills") >= strings.Index(out, "# Auto-loaded Skills") {
		t.Error("pinned section must appear before the auto section")
	}
}

func TestBuildAgentSkillBlocks_DedupAgainstPinned(t *testing.T) {
	// A single skill that is both pinned and trigger-matched must appear
	// exactly once — under the pinned header, not duplicated under auto.
	cli, _ := newPipelineCLI(t, map[string]string{
		"echo": `---
name: echo
description: triggers on echo
triggers: ["echo"]
---
echo body
`,
	})
	cli.skillHandler.Pin("echo")
	a := &AgentMode{cli: cli, logger: zap.NewNop()}
	out := a.buildAgentSkillBlocks("please echo this", "")

	if !strings.Contains(out, "# Pinned Skills") {
		t.Errorf("expected pinned header; got:\n%s", out)
	}
	if strings.Contains(out, "# Auto-loaded Skills") {
		t.Errorf("auto-loaded header must not appear when the only candidate was already pinned; got:\n%s", out)
	}
	if occurrences := strings.Count(out, "## Skill: echo"); occurrences != 1 {
		t.Errorf("expected skill 'echo' to render exactly once, found %d occurrences", occurrences)
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

func TestBuildAgentSkillBlocks_PinnedModelHintWinsOverAuto(t *testing.T) {
	// Pinned skill carries opus; trigger-matched skill carries sonnet.
	// pickSkillModelAndEffort's "first non-empty wins" rule + the
	// pinned-then-auto ordering means opus wins.
	cli, _ := newPipelineCLI(t, map[string]string{
		"pinned-opus": `---
name: pinned-opus
description: pinned, prefers opus
model: opus
---
body
`,
		"auto-sonnet": `---
name: auto-sonnet
description: trigger fires sonnet
model: sonnet
triggers: ["fire"]
---
body
`,
	})
	cli.skillHandler.Pin("pinned-opus")
	a := &AgentMode{cli: cli, logger: zap.NewNop()}
	_ = a.buildAgentSkillBlocks("please fire this", "")

	if a.skillModelHint != "opus" {
		t.Errorf("expected pinned opus to win; got %q", a.skillModelHint)
	}
}
