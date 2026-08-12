/*
 * ChatCLI - MCP prompts primitive: slash-command catalog tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package rpcserve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// commandPromptBackend wraps fakeBackend with the OPTIONAL CommandPrompter
// capability — exactly how cmd/rpcserve.go's rpcBackend gains it.
type commandPromptBackend struct {
	*fakeBackend
}

func (b *commandPromptBackend) CommandPrompts() []CommandPromptInfo {
	return []CommandPromptInfo{
		{Name: "review-pr", Description: "Review a pull request", ArgumentHint: "<pr-number> [focus]"},
	}
}

func (b *commandPromptBackend) CommandPromptRender(_ context.Context, name, args string) (string, bool) {
	if name != "review-pr" {
		return "", false
	}
	return "Review PR " + args + " thoroughly.", true
}

func TestMCPPrompts_ListIncludesCommandsWithArguments(t *testing.T) {
	m := NewMCP(&commandPromptBackend{&fakeBackend{}}, "chatcli", "test")

	res, rpcErr := m.Handle(context.Background(), "prompts/list", nil)
	if rpcErr != nil {
		t.Fatalf("prompts/list: %v", rpcErr)
	}
	raw, _ := json.Marshal(res)
	out := string(raw)

	if !strings.Contains(out, "deploy-checklist") {
		t.Error("skills must keep serving through prompts/list")
	}
	if !strings.Contains(out, "review-pr") {
		t.Error("slash commands must join prompts/list")
	}
	// MCP-spec arguments field: clients render an input for "args".
	// (json.Marshal escapes angle brackets, so match the hint's core.)
	if !strings.Contains(out, `"arguments"`) || !strings.Contains(out, "pr-number") {
		t.Errorf("command prompt must carry the arguments field with the hint: %s", out)
	}
}

func TestMCPPrompts_GetResolvesCommandFirstThenSkill(t *testing.T) {
	m := NewMCP(&commandPromptBackend{&fakeBackend{}}, "chatcli", "test")

	// Command with the "args" argument.
	params, _ := json.Marshal(map[string]interface{}{
		"name": "review-pr", "arguments": map[string]string{"args": "1326 security"},
	})
	res, rpcErr := m.Handle(context.Background(), "prompts/get", params)
	if rpcErr != nil {
		t.Fatalf("prompts/get command: %v", rpcErr)
	}
	raw, _ := json.Marshal(res)
	if !strings.Contains(string(raw), "Review PR 1326 security thoroughly.") {
		t.Errorf("command expansion with args failed: %s", raw)
	}

	// Skill fallback keeps working.
	params, _ = json.Marshal(map[string]interface{}{"name": "deploy-checklist"})
	res, rpcErr = m.Handle(context.Background(), "prompts/get", params)
	if rpcErr != nil {
		t.Fatalf("prompts/get skill: %v", rpcErr)
	}
	raw, _ = json.Marshal(res)
	if !strings.Contains(string(raw), "Deploy checklist") {
		t.Errorf("skill prompt must still resolve: %s", raw)
	}

	// Unknown name is a clean invalid-params error.
	params, _ = json.Marshal(map[string]interface{}{"name": "nope"})
	if _, rpcErr = m.Handle(context.Background(), "prompts/get", params); rpcErr == nil {
		t.Error("unknown prompt must error")
	}
}

func TestMCPPrompts_BackendWithoutCapabilityUnchanged(t *testing.T) {
	m := NewMCP(&fakeBackend{}, "chatcli", "test")

	res, rpcErr := m.Handle(context.Background(), "prompts/list", nil)
	if rpcErr != nil {
		t.Fatalf("prompts/list: %v", rpcErr)
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), "review-pr") {
		t.Error("a backend without CommandPrompter must serve skills only")
	}
}
