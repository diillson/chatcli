/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/moa"
	"go.uber.org/zap"
)

func TestMoaLead(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain verdict", "Yes, migrate in phases. Start with stateless pools.", "Yes, migrate in phases."},
		{"bold label", "**Short answer:** not as a drop-in swap. Here is why.", "Short answer: not as a drop-in swap."},
		{"skips heading and rule", "## Verdict\n\n---\n\nKeep the autoscaler for now. Revisit later.", "Keep the autoscaler for now."},
		{"skips fence and table", "```yaml\nkind: NodePool\n```\n| a | b |\n> Depends on the workload mix. More below.", "Depends on the workload mix."},
		{"bullet", "- Migrate gradually, one node pool at a time", "Migrate gradually, one node pool at a time"},
		{"short first sentence kept whole", "Yes. Migrate gradually.", "Yes. Migrate gradually."},
		{"version dot is not a sentence end", "Karpenter v1.2 is the target for this cluster.", "Karpenter v1.2 is the target for this cluster."},
		{"no prose", "```\ncode\n```\n---\n123", ""},
	}
	for _, c := range cases {
		if got := moaLead(c.in); got != c.want {
			t.Errorf("%s: moaLead = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMoaClip(t *testing.T) {
	if got := moaClip("short", 80); got != "short" {
		t.Fatalf("moaClip kept text = %q", got)
	}
	long := strings.Repeat("x", 100)
	got := moaClip(long, 30)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) != 30 {
		t.Fatalf("moaClip(100 chars, 30) = %q (%d runes)", got, len([]rune(got)))
	}
	if got := moaClip(long, 5); len([]rune(got)) != 20 {
		t.Fatalf("moaClip floors the width at 20, got %d runes", len([]rune(got)))
	}
}

func TestMoaOneLine(t *testing.T) {
	in := "API error: status 404 - {\n  \"error\": {\n    \"code\": 404\n  }\n}"
	if got := moaOneLine(in); got != `API error: status 404 - { "error": { "code": 404 } }` {
		t.Fatalf("moaOneLine = %q", got)
	}
}

func TestMoaPanelMode(t *testing.T) {
	for env, want := range map[string]string{"": moaPanelLead, "LEAD": moaPanelLead, " full ": moaPanelFull, "off": moaPanelOff, "bogus": moaPanelLead} {
		t.Setenv("CHATCLI_MOA_PANEL", env)
		if got := moaPanelMode(); got != want {
			t.Errorf("CHATCLI_MOA_PANEL=%q: mode %q, want %q", env, got, want)
		}
	}
}

func TestMoaPanelPrinter(t *testing.T) {
	c := newMoaCLI(&moaFakeManager{minimalManager: &minimalManager{}}, "agg", "m")
	ok := moa.RefResult{Ref: moa.Ref{Provider: "xai", Model: "grok"}, Output: "Not as a drop-in swap. Phase it in."}
	failed := moa.RefResult{Ref: moa.Ref{Provider: "googleai", Model: "gemini"}, Err: errors.New("API error: status 404 - {\n \"code\": 404\n}")}
	empty := moa.RefResult{Ref: moa.Ref{Provider: "openai", Model: "gpt"}}

	lead := captureStdout(t, func() {
		p := c.moaPanelPrinter(moaPanelLead, time.Now())
		p(ok)
		p(failed)
		p(empty)
	})
	for _, want := range []string{"xai:grok", "Not as a drop-in swap.", "googleai:gemini", `status 404 - { "code": 404 }`, "openai:gpt"} {
		if !strings.Contains(lead, want) {
			t.Errorf("lead output misses %q:\n%s", want, lead)
		}
	}
	if strings.Contains(lead, "Phase it in.") {
		t.Errorf("lead mode printed beyond the first sentence:\n%s", lead)
	}

	off := captureStdout(t, func() { c.moaPanelPrinter(moaPanelOff, time.Now())(ok) })
	if !strings.Contains(off, "xai:grok") || strings.Contains(off, "drop-in") {
		t.Errorf("off mode should list the member only:\n%s", off)
	}

	full := captureStdout(t, func() { c.moaPanelPrinter(moaPanelFull, time.Now())(ok) })
	if !strings.Contains(full, "Phase it in.") {
		t.Errorf("full mode should print the whole answer:\n%s", full)
	}
}

func TestRunMoACommand_UsageAndNoModels(t *testing.T) {
	c := newMoaCLI(&moaFakeManager{minimalManager: &minimalManager{}}, "agg", "m")
	t.Setenv("CHATCLI_MOA_MODELS", "")
	var err error
	captureStdout(t, func() { err = c.runMoACommand(context.Background(), "/moa   ") })
	if !errors.Is(err, errMoAUsage) {
		t.Fatalf("empty prompt: err = %v, want errMoAUsage", err)
	}
	captureStdout(t, func() { err = c.runMoACommand(context.Background(), "/moa which autoscaler?") })
	if !errors.Is(err, errMoANoModels) {
		t.Fatalf("no providers: err = %v, want errMoANoModels", err)
	}
	out := captureStdout(t, func() { c.handleMoACommand(context.Background(), "/moa") })
	if !strings.Contains(out, "ERR") {
		t.Fatalf("handleMoACommand should report the error:\n%s", out)
	}
}

func TestRunMoACommand_PrintsPanelThenSynthesis(t *testing.T) {
	mgr := &moaFakeManager{
		minimalManager: &minimalManager{providers: []string{"a", "b", "agg"}},
		answers:        map[string]string{"a": "Yes, migrate. Details follow.", "b": "Not yet. Wait a quarter.", "agg": "FINAL SYNTHESIS"},
	}
	c := newMoaCLI(mgr, "agg", "m")
	ch, err := NewContextHandlerAt(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatalf("NewContextHandlerAt: %v", err)
	}
	c.contextHandler = ch
	t.Setenv("CHATCLI_MOA_MODELS", "a:m1,b:m2")
	t.Setenv("CHATCLI_MOA_PANEL", "")
	out := captureStdout(t, func() { err = c.runMoACommand(context.Background(), "/moa replace the autoscaler?") })
	if err != nil {
		t.Fatalf("runMoACommand: %v", err)
	}
	ia, ib, isyn, ifinal := strings.Index(out, "Yes, migrate."), strings.Index(out, "Not yet."), strings.Index(out, "agg:m"), strings.Index(out, "FINAL SYNTHESIS")
	if ia < 0 || ib < 0 || isyn < 0 || ifinal < 0 || isyn > ifinal || ia > isyn || ib > isyn {
		t.Fatalf("expected both positions, then the synthesizing line, then the answer:\n%s", out)
	}
	if n := len(c.history); n != 2 || c.history[1].Content != "FINAL SYNTHESIS" {
		t.Fatalf("history should record the turn, got %d messages", n)
	}
}

func TestBuildSynthesisPrompt_KeepsDivergence(t *testing.T) {
	p := buildSynthesisPrompt("q", []moaResult{{label: "a", answer: "x"}, {label: "b", answer: "y"}})
	if !strings.Contains(p, moa.DivergenceInstruction) {
		t.Fatalf("@moa synthesis prompt should ask for the divergence section:\n%s", p)
	}
}
