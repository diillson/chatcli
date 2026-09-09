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
		"workspace stable text",
		"workspace turn text",
		"skills text",
		"orchestrator text",
		"channels text",
		"dynamic text",
	)
	if msg.Role != "system" {
		t.Errorf("Role = %q, want system", msg.Role)
	}
	// 4 stable (core, tools, orchestrator, workspace-stable) + 4 volatile
	// (workspace-turn, skills, channels, dynamic) = 8 parts.
	if got := len(msg.SystemParts); got != 8 {
		t.Fatalf("len(SystemParts) = %d, want 8", got)
	}
	// The first four parts are the stable cached prefix.
	for i := 0; i < 4; i++ {
		if msg.SystemParts[i].CacheControl == nil {
			t.Errorf("stable SystemParts[%d] missing CacheControl", i)
		}
	}
	// The trailing four are volatile and MUST NOT carry a cache hint.
	for i := 4; i < 8; i++ {
		if msg.SystemParts[i].CacheControl != nil {
			t.Errorf("volatile SystemParts[%d] must be uncached, got %+v",
				i, msg.SystemParts[i].CacheControl)
		}
	}
	// Flat content emits stable prefix first, then the volatile suffix.
	wantOrder := []string{
		"core text", "tools text", "orchestrator text", "workspace stable text", // stable
		"workspace turn text", "skills text", "channels text", "dynamic text", // volatile
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
	msg := buildAgentSystemMessage("core", "", "", "", "", "", "", "")
	if len(msg.SystemParts) != 1 {
		t.Fatalf("want 1 part, got %d", len(msg.SystemParts))
	}
	if msg.SystemParts[0].Text != "core" {
		t.Errorf("part = %q", msg.SystemParts[0].Text)
	}
}

func TestBuildAgentSystemMessage_OnlyChannelsBlock(t *testing.T) {
	msg := buildAgentSystemMessage("", "", "", "", "", "", "## Channels", "")
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
	msg := buildAgentSystemMessage("", "", "", "", "", "", "", "now: 2026-06-01")
	if len(msg.SystemParts) != 1 {
		t.Fatalf("want 1 part, got %d", len(msg.SystemParts))
	}
	if msg.SystemParts[0].CacheControl != nil {
		t.Errorf("dynamic block must be uncached, got %+v", msg.SystemParts[0].CacheControl)
	}
}

// The QUERY-DRIVEN half of the workspace stays volatile: it carries no cache
// hint and trails the stable prefix. (Its turn-independent half is cached —
// see TestBuildAgentSystemMessage_StableWorkspaceIsCached.)
func TestBuildAgentSystemMessage_WorkspaceIsVolatile(t *testing.T) {
	msg := buildAgentSystemMessage("core", "tools", "", "workspace mem", "", "orch", "", "")
	// stable: core, tools, orch (3) + volatile: workspace-turn (1) = 4
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

// TestBuildAgentSystemMessage_StableWorkspaceIsCached: the half that does not
// depend on the query earns a breakpoint, and lands AFTER the blocks that
// already had one so their prefix stays byte-identical.
func TestBuildAgentSystemMessage_StableWorkspaceIsCached(t *testing.T) {
	msg := buildAgentSystemMessage("core", "tools", "bootstrap + index", "matched rules", "", "orch", "", "")

	var stableIdx, turnIdx = -1, -1
	for i, p := range msg.SystemParts {
		switch {
		case strings.Contains(p.Text, "bootstrap + index"):
			stableIdx = i
		case strings.Contains(p.Text, "matched rules"):
			turnIdx = i
		}
	}
	if stableIdx == -1 || turnIdx == -1 {
		t.Fatalf("blocks missing: stable=%d turn=%d", stableIdx, turnIdx)
	}
	if msg.SystemParts[stableIdx].CacheControl == nil {
		t.Error("the turn-independent workspace half must carry a cache hint")
	}
	if msg.SystemParts[turnIdx].CacheControl != nil {
		t.Error("the query-driven half must stay uncached")
	}
	if stableIdx > turnIdx {
		t.Error("a cached block after a volatile one breaks the contiguous prefix")
	}
	// core/tools/orchestrator keep their positions, so the prefix they earned
	// is not shifted by the new block.
	if !strings.Contains(msg.SystemParts[0].Text, "core") ||
		!strings.Contains(msg.SystemParts[1].Text, "tools") ||
		!strings.Contains(msg.SystemParts[2].Text, "orch") {
		t.Errorf("stable prefix reordered: %+v", msg.SystemParts)
	}
}
