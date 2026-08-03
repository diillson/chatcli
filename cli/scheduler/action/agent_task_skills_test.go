/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package action

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/cli/scheduler"
)

// skillCaptureBridge fakes a skill-aware CLIBridge, recording which entry
// point the executor picked and what it passed.
type skillCaptureBridge struct {
	scheduler.CLIBridge
	plainCalled bool
	gotTask     string
	gotSkills   []string
}

func (b *skillCaptureBridge) RunAgentTask(_ context.Context, task, _ string, _ bool) (string, error) {
	b.plainCalled = true
	b.gotTask = task
	return "plain", nil
}

func (b *skillCaptureBridge) RunAgentTaskWithSkills(_ context.Context, task, _ string, _ bool, skills []string) (string, error) {
	b.gotTask = task
	b.gotSkills = skills
	return "with-skills", nil
}

// plainBridge fakes a bridge WITHOUT the SkillAwareBridge capability.
type plainBridge struct {
	scheduler.CLIBridge
	called bool
}

func (b *plainBridge) RunAgentTask(_ context.Context, _, _ string, _ bool) (string, error) {
	b.called = true
	return "plain", nil
}

// TestAgentTaskUsesSkillAwareBridge pins the routing: a payload with skills
// goes through RunAgentTaskWithSkills when the bridge supports it.
func TestAgentTaskUsesSkillAwareBridge(t *testing.T) {
	bridge := &skillCaptureBridge{CLIBridge: scheduler.NewNoopBridge()}
	env := &scheduler.ExecEnv{Bridge: bridge}
	act := scheduler.Action{Type: scheduler.ActionAgentTask, Payload: map[string]any{
		"task":   "revisar card",
		"skills": []any{"clean-code", "database-design"},
	}}

	res := NewAgentTask().Execute(context.Background(), act, env)
	if res.Err != nil {
		t.Fatalf("execute: %v", res.Err)
	}
	if bridge.plainCalled {
		t.Error("skills payload must route through RunAgentTaskWithSkills, not RunAgentTask")
	}
	if len(bridge.gotSkills) != 2 || bridge.gotSkills[0] != "clean-code" {
		t.Errorf("skills not forwarded: %v", bridge.gotSkills)
	}
	if bridge.gotTask != "revisar card" {
		t.Errorf("task not forwarded: %q", bridge.gotTask)
	}
}

// TestAgentTaskFallsBackWithoutCapability pins graceful degradation: a
// bridge without SkillAwareBridge still runs the task via RunAgentTask.
func TestAgentTaskFallsBackWithoutCapability(t *testing.T) {
	bridge := &plainBridge{CLIBridge: scheduler.NewNoopBridge()}
	env := &scheduler.ExecEnv{Bridge: bridge}
	act := scheduler.Action{Type: scheduler.ActionAgentTask, Payload: map[string]any{
		"task":   "revisar card",
		"skills": []any{"clean-code"},
	}}

	res := NewAgentTask().Execute(context.Background(), act, env)
	if res.Err != nil {
		t.Fatalf("execute: %v", res.Err)
	}
	if !bridge.called {
		t.Error("bridge without the capability must fall back to RunAgentTask")
	}
}

// TestAgentTaskNoSkillsUsesPlainPath pins that a payload without skills
// never touches the capability even when the bridge has it.
func TestAgentTaskNoSkillsUsesPlainPath(t *testing.T) {
	bridge := &skillCaptureBridge{CLIBridge: scheduler.NewNoopBridge()}
	env := &scheduler.ExecEnv{Bridge: bridge}
	act := scheduler.Action{Type: scheduler.ActionAgentTask, Payload: map[string]any{"task": "t"}}

	if res := NewAgentTask().Execute(context.Background(), act, env); res.Err != nil {
		t.Fatalf("execute: %v", res.Err)
	}
	if !bridge.plainCalled {
		t.Error("payload without skills must use RunAgentTask")
	}
}
