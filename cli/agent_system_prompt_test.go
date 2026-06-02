/*
 * ChatCLI - tests for buildAgentSystemMessage
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"
)

func TestBuildAgentSystemMessage_AllBlocksPresent(t *testing.T) {
	msg := buildAgentSystemMessage(
		"core text",
		"tools text",
		"workspace text",
		"skills text",
		"orchestrator text",
		"channels text",
		"dynamic text",
	)
	if msg.Role != "system" {
		t.Errorf("Role = %q, want system", msg.Role)
	}
	// 3 stable (core, tools, orchestrator) + 4 volatile
	// (workspace, skills, channels, dynamic) = 7 parts.
	if got := len(msg.SystemParts); got != 7 {
		t.Fatalf("len(SystemParts) = %d, want 7", got)
	}
	// The first three parts are the stable cached prefix.
	for i := 0; i < 3; i++ {
		if msg.SystemParts[i].CacheControl == nil {
			t.Errorf("stable SystemParts[%d] missing CacheControl", i)
		}
	}
	// The trailing four are volatile and MUST NOT carry a cache hint.
	for i := 3; i < 7; i++ {
		if msg.SystemParts[i].CacheControl != nil {
			t.Errorf("volatile SystemParts[%d] must be uncached, got %+v",
				i, msg.SystemParts[i].CacheControl)
		}
	}
	// Flat content emits stable prefix first, then the volatile suffix.
	wantOrder := []string{
		"core text", "tools text", "orchestrator text", // stable
		"workspace text", "skills text", "channels text", "dynamic text", // volatile
	}
	prev := -1
	for _, w := range wantOrder {
		i := strings.Index(msg.Content, w)
		if i <= prev {
			t.Errorf("block %q not after previous in Content", w)
		}
		prev = i
	}
}

func TestBuildAgentSystemMessage_OmitsEmptyBlocks(t *testing.T) {
	msg := buildAgentSystemMessage("core", "", "", "", "", "", "")
	if len(msg.SystemParts) != 1 {
		t.Fatalf("want 1 part, got %d", len(msg.SystemParts))
	}
	if msg.SystemParts[0].Text != "core" {
		t.Errorf("part = %q", msg.SystemParts[0].Text)
	}
}

func TestBuildAgentSystemMessage_OnlyChannelsBlock(t *testing.T) {
	msg := buildAgentSystemMessage("", "", "", "", "", "## Channels", "")
	if len(msg.SystemParts) != 1 {
		t.Fatalf("want 1 part, got %d", len(msg.SystemParts))
	}
	if msg.SystemParts[0].CacheControl != nil {
		t.Errorf("channels-only must remain uncached")
	}
}

// The wall-clock timestamp block is the most volatile input and must never
// carry a cache hint, even when it is the only block present.
func TestBuildAgentSystemMessage_DynamicBlockUncached(t *testing.T) {
	msg := buildAgentSystemMessage("", "", "", "", "", "", "now: 2026-06-01")
	if len(msg.SystemParts) != 1 {
		t.Fatalf("want 1 part, got %d", len(msg.SystemParts))
	}
	if msg.SystemParts[0].CacheControl != nil {
		t.Errorf("dynamic block must be uncached, got %+v", msg.SystemParts[0].CacheControl)
	}
}

// Workspace (memory) is volatile under the new layout: it carries no cache
// hint and trails the stable prefix.
func TestBuildAgentSystemMessage_WorkspaceIsVolatile(t *testing.T) {
	msg := buildAgentSystemMessage("core", "tools", "workspace mem", "", "orch", "", "")
	// stable: core, tools, orch (3) + volatile: workspace (1) = 4
	if len(msg.SystemParts) != 4 {
		t.Fatalf("want 4 parts, got %d", len(msg.SystemParts))
	}
	// Locate the workspace block and assert it is uncached and after the
	// stable prefix.
	var wsIdx = -1
	for i, p := range msg.SystemParts {
		if strings.Contains(p.Text, "workspace mem") {
			wsIdx = i
			break
		}
	}
	if wsIdx == -1 {
		t.Fatal("workspace block not found")
	}
	if msg.SystemParts[wsIdx].CacheControl != nil {
		t.Errorf("workspace block must be uncached, got %+v", msg.SystemParts[wsIdx].CacheControl)
	}
	if wsIdx < 3 {
		t.Errorf("workspace block must trail the stable prefix; got index %d", wsIdx)
	}
}
