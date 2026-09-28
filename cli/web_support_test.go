/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
)

func webTestCLI(t *testing.T) *ChatCLI {
	t.Helper()
	i18n.Init()
	c := completerSkillFixture(t, map[string]string{
		"hidden":   "---\nname: hidden\ndescription: not invocable\n---\nbody\n",
		"runnable": "---\nname: runnable\ndescription: invocable\nuser-invocable: true\n---\nbody\n",
	})
	c.commandHandler = NewCommandHandler(c)
	return c
}

// The web surface adds its commands on top of the ACP allowlist without
// widening it: ACP keeps its list, the page gets the extras with resolved
// descriptions.
func TestListWebCommands_ExtendsWithoutWideningACP(t *testing.T) {
	c := webTestCLI(t)
	acp := map[string]bool{}
	for _, cmd := range c.ListACPCommands() {
		acp[cmd.Name] = true
	}
	web := map[string]ACPCommandInfo{}
	for _, cmd := range c.ListWebCommands() {
		web[cmd.Name] = cmd
	}
	for _, name := range []string{"clear", "retry", "rewind", "plan", "jobs", "schedule", "hooks", "lsp"} {
		if acp[name] {
			t.Errorf("%q must not be advertised over ACP", name)
		}
		got, ok := web[name]
		if !ok {
			t.Errorf("%q must be advertised to the web page", name)
			continue
		}
		if got.Description == "" || strings.HasPrefix(got.Description, "web.command.") || strings.HasPrefix(got.Description, "complete.") {
			t.Errorf("%q description unresolved: %q", name, got.Description)
		}
	}
	if !acp["memory"] || web["memory"].Name != "memory" {
		t.Fatal("the ACP allowlist must still be part of the web surface")
	}
	if web["rewind"].InputHint == "" || web["plan"].InputHint == "" || web["clear"].InputHint != "" {
		t.Fatalf("hints: rewind=%q plan=%q clear=%q", web["rewind"].InputHint, web["plan"].InputHint, web["clear"].InputHint)
	}
}

// /jobs only reads from the page; "/schedule list" is the listing.
func TestRunWebCommandRPC_SchedulerRules(t *testing.T) {
	c := webTestCLI(t)
	ctx := context.Background()
	for _, line := range []string{"/jobs cancel j1", "/jobs pause j1", "/jobs gc", "/jobs daemon start"} {
		out, err := c.RunWebCommandRPC(ctx, line)
		if err != nil || !strings.Contains(out, "/jobs list") || strings.Contains(out, "web.command") {
			t.Fatalf("%s: %q %v", line, out, err)
		}
	}
	listing, err := c.RunWebCommandRPC(ctx, "/jobs list")
	if err != nil {
		t.Fatal(err)
	}
	viaSchedule, err := c.RunWebCommandRPC(ctx, "/schedule list")
	if err != nil || viaSchedule != listing {
		t.Fatalf("/schedule list must be the /jobs listing: %q vs %q (%v)", viaSchedule, listing, err)
	}
	// Anything else is the terminal's own command.
	out, err := c.RunWebCommandRPC(ctx, "/version")
	if err != nil || out == "" {
		t.Fatalf("/version: %q %v", out, err)
	}
}

func TestPlanCommandRPC(t *testing.T) {
	c := webTestCLI(t)
	ctx := context.Background()
	cases := []struct{ line, mode, task string }{
		{"/plan", "", ""},
		{"/plan coder", "", ""},
		{"/plan agent", "agent", ""},
		{"/plan fix the build", "agent", "fix the build"},
		{"/plan agent fix the build", "agent", "fix the build"},
		{"/plan coder add tests", "coder", "add tests"},
		{"/plan preview refactor x", "agent", "refactor x"},
		{"/plan dry refactor x", "agent", "refactor x"},
	}
	for _, tc := range cases {
		c.pendingPlanFirst, c.pendingPlanDryRun = false, false
		mode, task, notice := c.PlanCommandRPC(ctx, tc.line)
		if mode != tc.mode || task != tc.task {
			t.Errorf("%s: mode=%q task=%q, want %q %q", tc.line, mode, task, tc.mode, tc.task)
		}
		if !c.pendingPlanFirst {
			t.Errorf("%s must arm Plan-First", tc.line)
		}
		if tc.line == "/plan" && !strings.Contains(notice, "plan") {
			t.Errorf("bare /plan must say it armed: %q", notice)
		}
		if strings.HasPrefix(tc.line, "/plan preview") && !c.pendingPlanDryRun {
			t.Errorf("%s must arm the dry run", tc.line)
		}
	}
	// A preview without a task keeps nothing armed, as in the terminal.
	c.pendingPlanFirst = false
	if mode, _, notice := c.PlanCommandRPC(ctx, "/plan preview"); mode != "" || c.pendingPlanFirst || notice == "" {
		t.Fatalf("bare preview: mode=%q armed=%v notice=%q", mode, c.pendingPlanFirst, notice)
	}
}

func TestStageSkillRPC(t *testing.T) {
	c := webTestCLI(t)
	if _, _, ok := c.StageSkillRPC("nosuch", ""); ok {
		t.Fatal("an unknown name is not a skill")
	}
	if _, _, ok := c.StageSkillRPC("memory", ""); ok {
		t.Fatal("a built-in command name is never a skill")
	}
	if _, refused, ok := c.StageSkillRPC("hidden", ""); !ok || refused == "" || c.pendingManualSkill != nil {
		t.Fatalf("hidden: ok=%v refused=%q staged=%v", ok, refused, c.pendingManualSkill)
	}
	prompt, refused, ok := c.StageSkillRPC("runnable", "the diff")
	if !ok || refused != "" || prompt != "the diff" || c.pendingManualSkill == nil || c.pendingManualSkill.Name != "runnable" || c.pendingManualSkillArgs != "the diff" {
		t.Fatalf("runnable: ok=%v refused=%q prompt=%q staged=%+v", ok, refused, prompt, c.pendingManualSkill)
	}
	if prompt, _, _ := c.StageSkillRPC("runnable", ""); !strings.Contains(prompt, "runnable") {
		t.Fatalf("no args must synthesize the terminal's prompt: %q", prompt)
	}
}
