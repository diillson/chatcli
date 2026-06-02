/*
 * ChatCLI - tests for the stable memory index (pull-model "map").
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"strings"
	"testing"
)

// seedManager populates a manager with a profile, topics, projects and facts
// via the same extraction path production uses.
func seedManager(t *testing.T) *Manager {
	t.Helper()
	mgr := NewManager(t.TempDir(), DefaultConfig(), testLogger())
	mgr.ProcessExtraction(`## LONGTERM
- ChatCLI uses Go with a plugin system
- embed.FS needs '/' separators on Windows

## PROFILE_UPDATE
name=Edilson
role=SRE
expertise_level=expert
company=Acme

## TOPICS
scheduler, oauth, memory, cache

## PROJECTS
project_name=chatcli
project_path=/Users/edilson/GolandProjects/chatcli
project_status=active
project_technologies=Go`)
	return mgr
}

func TestGetMemoryIndex_DigestContents(t *testing.T) {
	mgr := seedManager(t)
	idx := mgr.GetMemoryIndex(0)
	if idx == "" {
		t.Fatal("expected non-empty index")
	}
	for _, want := range []string{"# Memory Index", "Profile:", "Edilson", "SRE", "Projects:", "chatcli", "Topics:", "Facts:"} {
		if !strings.Contains(idx, want) {
			t.Errorf("index missing %q\n---\n%s", want, idx)
		}
	}
	// The digest must NOT dump full fact bodies — only a tally.
	if strings.Contains(idx, "embed.FS needs") {
		t.Errorf("index leaked a full fact body; should only tally counts:\n%s", idx)
	}
}

func TestGetMemoryIndex_Stable(t *testing.T) {
	mgr := seedManager(t)
	// Stability: two calls with no mutation in between yield identical text
	// (no timestamps, no hint-dependence) — the property that makes it
	// cache-friendly.
	if a, b := mgr.GetMemoryIndex(0), mgr.GetMemoryIndex(0); a != b {
		t.Errorf("index not stable across calls:\n%q\nvs\n%q", a, b)
	}
}

func TestGetMemoryIndex_BudgetCap(t *testing.T) {
	mgr := seedManager(t)
	const budget = 40
	idx := mgr.GetMemoryIndex(budget)
	if n := len([]rune(idx)); n > budget {
		t.Errorf("index exceeded budget: %d runes > %d", n, budget)
	}
}

func TestGetMemoryIndex_EmptyMemory(t *testing.T) {
	mgr := NewManager(t.TempDir(), DefaultConfig(), testLogger())
	if idx := mgr.GetMemoryIndex(0); idx != "" {
		t.Errorf("empty memory should yield empty index, got %q", idx)
	}
}

func TestFactTally_OrdersByCount(t *testing.T) {
	facts := []*Fact{
		{Category: "gotcha"},
		{Category: "architecture"},
		{Category: "architecture"},
		{Category: ""}, // folds into "general"
	}
	got := factTally(facts)
	if !strings.HasPrefix(got, "4 stored (") {
		t.Errorf("tally should report total first; got %q", got)
	}
	// architecture (2) must appear before gotcha (1) and general (1).
	ai := strings.Index(got, "architecture")
	gi := strings.Index(got, "gotcha")
	if ai == -1 || gi == -1 || ai > gi {
		t.Errorf("categories not ordered by descending count: %q", got)
	}
	if !strings.Contains(got, "general 1") {
		t.Errorf("empty category should fold into general: %q", got)
	}
}
