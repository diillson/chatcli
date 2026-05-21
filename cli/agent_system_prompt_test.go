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
	)
	if msg.Role != "system" {
		t.Errorf("Role = %q, want system", msg.Role)
	}
	// 4 cached + 1 volatile = 5 parts
	if got := len(msg.SystemParts); got != 5 {
		t.Fatalf("len(SystemParts) = %d, want 5", got)
	}
	// First four are cached
	for i := 0; i < 4; i++ {
		if msg.SystemParts[i].CacheControl == nil {
			t.Errorf("SystemParts[%d] missing CacheControl", i)
		}
	}
	// Channels block is last and uncached
	last := msg.SystemParts[4]
	if last.CacheControl != nil {
		t.Errorf("channels block must be uncached, got %+v", last.CacheControl)
	}
	if last.Text != "channels text" {
		t.Errorf("channels block text = %q", last.Text)
	}
	// Flat content has every block in order
	wantOrder := []string{"core text", "tools text", "workspace text",
		"skills text", "orchestrator text", "channels text"}
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
	msg := buildAgentSystemMessage("core", "", "", "", "", "")
	if len(msg.SystemParts) != 1 {
		t.Fatalf("want 1 part, got %d", len(msg.SystemParts))
	}
	if msg.SystemParts[0].Text != "core" {
		t.Errorf("part = %q", msg.SystemParts[0].Text)
	}
}

func TestBuildAgentSystemMessage_OnlyChannelsBlock(t *testing.T) {
	msg := buildAgentSystemMessage("", "", "", "", "", "## Channels")
	if len(msg.SystemParts) != 1 {
		t.Fatalf("want 1 part, got %d", len(msg.SystemParts))
	}
	if msg.SystemParts[0].CacheControl != nil {
		t.Errorf("channels-only must remain uncached")
	}
}

func TestBuildAgentSystemMessage_MergesSkillsAndOrchestrator(t *testing.T) {
	msg := buildAgentSystemMessage(
		"core", "tools", "workspace",
		"skills part",
		"orchestrator part",
		"",
	)
	// 4 cached blocks (core, tools, workspace, skills+orchestrator merged)
	if len(msg.SystemParts) != 4 {
		t.Fatalf("want 4 parts (skills+orch merged), got %d", len(msg.SystemParts))
	}
	last := msg.SystemParts[3]
	if !strings.Contains(last.Text, "skills part") || !strings.Contains(last.Text, "orchestrator part") {
		t.Errorf("tail block missing merged content: %q", last.Text)
	}
}
