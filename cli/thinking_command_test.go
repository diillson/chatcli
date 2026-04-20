/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/llm/client"
)

func TestApplyThinkingOverride_NotSet(t *testing.T) {
	cli := &ChatCLI{}
	got, overridden := cli.applyThinkingOverride(client.EffortHigh)
	if overridden {
		t.Fatalf("no override set must return overridden=false")
	}
	if got != client.EffortHigh {
		t.Fatalf("no override must return the skill effort unchanged; got %q", got)
	}
}

func TestApplyThinkingOverride_OffSuppressesSkillHint(t *testing.T) {
	cli := &ChatCLI{}
	cli.thinkingOverride = thinkingOverrideState{set: true, effort: client.EffortUnset}
	got, overridden := cli.applyThinkingOverride(client.EffortHigh)
	if !overridden {
		t.Fatalf("active override must return overridden=true")
	}
	if got != client.EffortUnset {
		t.Fatalf("explicit off must return EffortUnset so caller skips WithEffortHint; got %q", got)
	}
}

func TestApplyThinkingOverride_ExplicitTierWinsOverSkill(t *testing.T) {
	cli := &ChatCLI{}
	cli.thinkingOverride = thinkingOverrideState{set: true, effort: client.EffortMax}
	got, overridden := cli.applyThinkingOverride(client.EffortLow)
	if !overridden {
		t.Fatalf("active override must return overridden=true")
	}
	if got != client.EffortMax {
		t.Fatalf("explicit tier must win; got %q", got)
	}
}

func TestHandleThinkingCommand_AutoClearsOverride(t *testing.T) {
	cli := &ChatCLI{}
	cli.thinkingOverride = thinkingOverrideState{set: true, effort: client.EffortMax}
	cli.handleThinkingCommand("/thinking auto")
	if cli.thinkingOverride.set {
		t.Fatalf("/thinking auto must clear override")
	}
}

func TestHandleThinkingCommand_OffSetsExplicitOff(t *testing.T) {
	cli := &ChatCLI{}
	cli.handleThinkingCommand("/thinking off")
	if !cli.thinkingOverride.set || cli.thinkingOverride.effort != client.EffortUnset {
		t.Fatalf("/thinking off must produce {set:true, effort:Unset}; got %+v", cli.thinkingOverride)
	}
}

func TestHandleThinkingCommand_TierMapping(t *testing.T) {
	cases := map[string]client.SkillEffort{
		"/thinking on":     client.EffortHigh,
		"/thinking high":   client.EffortHigh,
		"/thinking max":    client.EffortMax,
		"/thinking medium": client.EffortMedium,
		"/thinking med":    client.EffortMedium,
		"/thinking low":    client.EffortLow,
	}
	for input, want := range cases {
		cli := &ChatCLI{}
		cli.handleThinkingCommand(input)
		if !cli.thinkingOverride.set || cli.thinkingOverride.effort != want {
			t.Errorf("%q → %+v, want effort=%q", input, cli.thinkingOverride, want)
		}
	}
}

func TestHandleThinkingCommand_BudgetMapsToTier(t *testing.T) {
	cases := map[string]client.SkillEffort{
		"/thinking budget=2000":  client.EffortHigh,   // < 4096 → default High
		"/thinking budget=5000":  client.EffortMedium, // ≥ 4096
		"/thinking budget=10000": client.EffortHigh,   // ≥ 8192
		"/thinking budget=20000": client.EffortMax,    // ≥ 16384
	}
	for input, want := range cases {
		cli := &ChatCLI{}
		cli.handleThinkingCommand(input)
		if !cli.thinkingOverride.set || cli.thinkingOverride.effort != want {
			t.Errorf("%q → %+v, want effort=%q", input, cli.thinkingOverride, want)
		}
	}
}

func TestHandleThinkingCommand_BudgetInvalidLeavesOverrideUnchanged(t *testing.T) {
	cli := &ChatCLI{}
	cli.thinkingOverride = thinkingOverrideState{set: true, effort: client.EffortMax}
	cli.handleThinkingCommand("/thinking budget=abc")
	if !cli.thinkingOverride.set || cli.thinkingOverride.effort != client.EffortMax {
		t.Fatalf("invalid budget must not corrupt override; got %+v", cli.thinkingOverride)
	}
}

func TestHandleThinkingCommand_UnknownArgLeavesOverrideUnchanged(t *testing.T) {
	cli := &ChatCLI{}
	cli.handleThinkingCommand("/thinking garbage")
	if cli.thinkingOverride.set {
		t.Fatalf("unknown arg must not set override; got %+v", cli.thinkingOverride)
	}
}

func TestHandleThinkingCommand_BareCommandIsReadOnly(t *testing.T) {
	cli := &ChatCLI{}
	prev := cli.thinkingOverride
	cli.handleThinkingCommand("/thinking")
	if cli.thinkingOverride != prev {
		t.Fatalf("bare /thinking must not mutate override; got %+v", cli.thinkingOverride)
	}
}
