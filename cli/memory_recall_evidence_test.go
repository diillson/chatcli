/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"go.uber.org/zap"
)

func TestFactEvidenced(t *testing.T) {
	terms := []string{"postgres", "pgbouncer", "pool", "timeout"}
	if factEvidenced(terms, "we tuned pgbouncer and the pool size") == false {
		t.Fatal("two of four terms must count as evidence")
	}
	if factEvidenced(terms, "we changed the theme color") {
		t.Fatal("no overlap must not count")
	}
	long := []string{"a1", "b2", "c3", "d4", "e5", "f6", "g7", "h8", "i9", "j0", "k1", "l2"}
	if factEvidenced(long, "a1 b2 appear") {
		t.Fatal("a 12-term fact needs more than two hits")
	}
	if !factEvidenced(long, "a1 b2 c3 d4 e5 appear") {
		t.Fatal("a third of the terms is enough")
	}
	if !factEvidenced([]string{"kubernetes"}, "deployed to Kubernetes today") {
		t.Fatal("single-term facts need that one term")
	}
}

func TestReinforceRecalledFacts_OnlyUsedFactsMoveAndTurnSetClears(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cli := newTenantTestCLI(t)
	cli.memoryStore = workspace.NewMemoryStore(t.TempDir(), zap.NewNop())
	mgr := cli.memoryStore.Manager()
	if !mgr.Facts.AddFact("The billing service uses pgbouncer with a pool of 40 connections", "architecture", nil) ||
		!mgr.Facts.AddFact("Release notes are published every Friday afternoon", "process", nil) {
		t.Fatal("add facts")
	}
	var used, unused *memory.Fact
	for _, f := range mgr.Facts.SearchBlendedMin([]string{"pgbouncer", "pool"}, nil, memory.DefaultRankWeights(), 0) {
		used = f
	}
	for _, f := range mgr.Facts.SearchBlendedMin([]string{"release", "friday"}, nil, memory.DefaultRankWeights(), 0) {
		unused = f
	}
	if used == nil || unused == nil {
		t.Fatal("facts must be findable")
	}
	beforeUsed, beforeUnused := used.AccessCount, unused.AccessCount

	cli.noteRecalledFacts([]*memory.Fact{used, unused})
	ids := cli.reinforceRecalledFacts("I raised the pgbouncer pool from 40 to 60 connections for billing.")
	if len(ids) != 1 || ids[0] != used.ID {
		t.Fatalf("only the fact the reply drew on is reinforced, got %v", ids)
	}
	if used.AccessCount <= beforeUsed || unused.AccessCount != beforeUnused {
		t.Fatalf("access counts: used %d→%d, unused %d→%d", beforeUsed, used.AccessCount, beforeUnused, unused.AccessCount)
	}
	if again := cli.reinforceRecalledFacts("pgbouncer pool"); again != nil {
		t.Fatal("the turn's set must clear after reinforcement")
	}
}
