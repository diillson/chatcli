/*
 * ChatCLI - Proactive auto-recall tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"strings"
	"testing"
)

func newAutoRecallCLI(t *testing.T) *ChatCLI {
	t.Helper()
	cli := newTestCLIWithMemory(t)
	mgr := cli.memoryStore.Manager()
	if !mgr.Facts.AddFact("embed.FS requires forward slashes, never filepath.Join", "gotcha", []string{"embed", "windows"}) {
		t.Fatal("seed fact 1")
	}
	if !mgr.Facts.AddFact("The scheduler daemon re-arms intervals in place", "architecture", []string{"scheduler"}) {
		t.Fatal("seed fact 2")
	}
	return cli
}

func TestMemoryAutoRecallBlock_MatchesHints(t *testing.T) {
	cli := newAutoRecallCLI(t)

	block := cli.memoryAutoRecallBlock([]string{"embed", "windows", "paths"})
	if !strings.Contains(block, "[MEMORY AUTO-RECALL]") {
		t.Fatalf("expected auto-recall header, got %q", block)
	}
	if !strings.Contains(block, "forward slashes") {
		t.Errorf("expected the matching gotcha, got %q", block)
	}
	if strings.Contains(block, "scheduler daemon") {
		t.Errorf("unrelated fact must not ride along, got %q", block)
	}
}

func TestMemoryAutoRecallBlock_EmptyCases(t *testing.T) {
	cli := newAutoRecallCLI(t)

	if got := cli.memoryAutoRecallBlock(nil); got != "" {
		t.Errorf("no hints must inject nothing (empty hints would dump ALL facts), got %q", got)
	}
	if got := cli.memoryAutoRecallBlock([]string{"kubernetes", "istio"}); got != "" {
		t.Errorf("no matching fact must inject nothing, got %q", got)
	}

	t.Setenv("CHATCLI_MEMORY_AUTORECALL", "off")
	if got := cli.memoryAutoRecallBlock([]string{"embed"}); got != "" {
		t.Errorf("disabled auto-recall must inject nothing, got %q", got)
	}

	bare := &ChatCLI{}
	if got := bare.memoryAutoRecallBlock([]string{"embed"}); got != "" {
		t.Errorf("no memory store must inject nothing, got %q", got)
	}
}

func TestMemoryAutoRecallBlock_CapsFactCount(t *testing.T) {
	cli := newAutoRecallCLI(t)
	mgr := cli.memoryStore.Manager()
	for _, c := range []string{"alpha", "beta", "gamma", "delta", "epsilon"} {
		mgr.Facts.AddFact("gateway detail "+c+" for routing", "pattern", []string{"gateway"})
	}

	block := cli.memoryAutoRecallBlock([]string{"gateway", "routing"})
	if block == "" {
		t.Fatal("expected a block")
	}
	lines := 0
	for _, l := range strings.Split(block, "\n") {
		if strings.HasPrefix(l, "- ") {
			lines++
		}
	}
	if lines > autoRecallMaxFacts {
		t.Errorf("block must cap at %d facts, got %d:\n%s", autoRecallMaxFacts, lines, block)
	}
}
