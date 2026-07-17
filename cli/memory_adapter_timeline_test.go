/*
 * ChatCLI - memory adapter timeline + temporal recall tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/workspace/memory"
)

func seedEpisodes(t *testing.T, cli *ChatCLI) {
	t.Helper()
	mgr := cli.memoryStore.Manager()
	threeMonthsAgo := time.Now().AddDate(0, -3, 0)
	if !mgr.Episodes.Add(memory.Episode{
		Date: threeMonthsAgo, Project: "/home/u/chatcli",
		Summary: "Fixed the OAuth refresh loop", Outcome: "merged as PR 1047",
		Refs: []string{"llm/anthropic/auth.go"},
	}) {
		t.Fatal("seed episode 1 failed")
	}
	if !mgr.Episodes.Add(memory.Episode{
		Date: time.Now().AddDate(0, 0, -1), Project: "/home/u/chatcli",
		Summary: "Added the tool tag alias to the parser", Outcome: "PR 1200",
	}) {
		t.Fatal("seed episode 2 failed")
	}
}

func TestMemoryAdapter_TimelineEndToEnd(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	seedEpisodes(t, cli)
	a := &memoryPluginAdapter{cli: cli}

	// Natural PT window inside the query narrows to the old episode only.
	out, err := a.Timeline("", "", "", "o que fizemos há 3 meses", 0)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if !strings.Contains(out, "OAuth refresh loop") {
		t.Errorf("expected the 3-months-ago episode, got: %s", out)
	}
	if strings.Contains(out, "tool tag alias") {
		t.Errorf("yesterday's episode must be outside the window: %s", out)
	}

	// Explicit ISO window bounds work.
	from := time.Now().AddDate(0, -3, -15).Format("2006-01")
	out, err = a.Timeline("chatcli", from, time.Now().Format("2006-01"), "", 0)
	if err != nil || !strings.Contains(out, "OAuth") {
		t.Errorf("ISO window timeline failed: %v / %s", err, out)
	}

	// No filters → everything, chronological.
	out, err = a.Timeline("", "", "", "", 0)
	if err != nil || !strings.Contains(out, "OAuth") || !strings.Contains(out, "tool tag alias") {
		t.Errorf("unfiltered timeline must list both: %v / %s", err, out)
	}
}

func TestMemoryAdapter_RecallWithTemporalQuery(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	seedEpisodes(t, cli)
	a := &memoryPluginAdapter{cli: cli}

	out, err := a.Recall("o que fizemos há 3 meses")
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(out, "## Timeline") || !strings.Contains(out, "OAuth refresh loop") {
		t.Errorf("temporal recall must prepend the timeline slice, got: %s", out)
	}
}

func TestParseTimelineBound(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	if start, end, ok := parseTimelineBound("2026-04", now); !ok || start.Month() != time.April || end.Month() != time.May {
		t.Errorf("ISO month bound: ok=%v start=%v end=%v", ok, start, end)
	}
	if start, _, ok := parseTimelineBound("2026-04-12", now); !ok || start.Day() != 12 {
		t.Errorf("ISO day bound failed")
	}
	if start, _, ok := parseTimelineBound("3 months ago", now); !ok || start.Month() != time.April {
		t.Errorf("natural bound: ok=%v start=%v", ok, start)
	}
	if _, _, ok := parseTimelineBound("", now); ok {
		t.Error("empty bound must not resolve")
	}
	if _, _, ok := parseTimelineBound("gibberish", now); ok {
		t.Error("unparseable bound must not resolve")
	}
}
