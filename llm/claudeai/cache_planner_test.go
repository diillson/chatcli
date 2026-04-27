/*
 * ChatCLI - Tests for the Anthropic cache breakpoint planner
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import "testing"

func block(text string, marked bool) map[string]interface{} {
	b := map[string]interface{}{"type": "text", "text": text}
	if marked {
		b["cache_control"] = map[string]string{"type": "ephemeral"}
	}
	return b
}

func countMarkers(blocks []map[string]interface{}) int {
	n := 0
	for _, b := range blocks {
		if _, ok := b["cache_control"]; ok {
			n++
		}
	}
	return n
}

func TestCoalesceUnderLimitIsNoop(t *testing.T) {
	in := []map[string]interface{}{
		block("a", true),
		block("b", true),
		block("c", false),
		block("d", true),
	}
	before := CacheBlocksCoalescedTotal()
	out := coalesceCacheControl(in, 4)
	if countMarkers(out) != 3 {
		t.Fatalf("expected 3 markers preserved, got %d", countMarkers(out))
	}
	if got := CacheBlocksCoalescedTotal() - before; got != 0 {
		t.Fatalf("expected 0 coalesced, got %d", got)
	}
}

func TestCoalesceDropsEarliestMarkers(t *testing.T) {
	in := []map[string]interface{}{
		block("p0", true), // earliest — should be dropped
		block("p1", true), // earliest — should be dropped
		block("p2", true),
		block("p3", true),
		block("p4", true),
		block("p5", true), // latest — kept
	}
	before := CacheBlocksCoalescedTotal()
	out := coalesceCacheControl(in, 4)
	if countMarkers(out) != 4 {
		t.Fatalf("expected 4 markers after coalesce, got %d", countMarkers(out))
	}
	if _, ok := out[0]["cache_control"]; ok {
		t.Fatal("expected earliest marker (p0) to be dropped")
	}
	if _, ok := out[1]["cache_control"]; ok {
		t.Fatal("expected second-earliest marker (p1) to be dropped")
	}
	if _, ok := out[5]["cache_control"]; !ok {
		t.Fatal("expected latest marker (p5) to be kept")
	}
	if got := CacheBlocksCoalescedTotal() - before; got != 2 {
		t.Fatalf("expected counter to advance by 2, got %d", got)
	}
}

func TestCoalesceRespectsBudgetForToolReserve(t *testing.T) {
	in := []map[string]interface{}{
		block("a", true),
		block("b", true),
		block("c", true),
		block("d", true),
	}
	out := coalesceCacheControl(in, 3) // tool path reserves 1 marker for tools
	if countMarkers(out) != 3 {
		t.Fatalf("expected 3 markers (budget=3), got %d", countMarkers(out))
	}
	if _, ok := out[0]["cache_control"]; ok {
		t.Fatal("expected earliest marker to be dropped first")
	}
}

func TestCoalescePreservesBlockContent(t *testing.T) {
	in := []map[string]interface{}{
		block("alpha", true),
		block("beta", true),
		block("gamma", true),
		block("delta", true),
		block("epsilon", true),
	}
	out := coalesceCacheControl(in, 4)
	if len(out) != 5 {
		t.Fatalf("expected 5 blocks preserved, got %d", len(out))
	}
	expected := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	for i, want := range expected {
		if out[i]["text"] != want {
			t.Fatalf("block %d: expected text %q, got %v", i, want, out[i]["text"])
		}
	}
}
