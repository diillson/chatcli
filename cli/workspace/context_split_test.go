/*
 * ChatCLI - lifetime split of the workspace context
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The split exists to move stable content into the cached prefix. It must
 * move it, not lose it: whatever the single-block builder produced still has
 * to reach the model, and nothing turn-dependent may end up on the stable
 * side, where it would invalidate the cache it was meant to fill.
 */
package workspace

import (
	"context"
	"strings"
	"testing"
)

// TestSplitCarriesEverythingTheSingleBlockDid: curation may relocate content,
// never drop it.
func TestSplitCarriesEverythingTheSingleBlockDid(t *testing.T) {
	cb := newModeBuilder(t)
	whole := cb.BuildWorkspaceContextMode(context.Background(), "q", nil, nil, "index", "recall hint")
	stable, turn := cb.SplitWorkspaceContextMode(context.Background(), "q", nil, nil, "index", "recall hint")

	if strings.TrimSpace(whole) == "" {
		t.Fatal("fixture produced no workspace context")
	}
	for _, chunk := range []string{"You are helpful", "recall hint"} {
		if !strings.Contains(whole, chunk) {
			t.Fatalf("fixture lost %q before the split even ran", chunk)
		}
		if !strings.Contains(stable+"\n"+turn, chunk) {
			t.Errorf("split dropped %q (stable=%q turn=%q)", chunk, stable, turn)
		}
	}
}

// TestSplitKeepsTheStableSideTurnIndependent is the cache invariant: the
// stable side must be byte-identical for two different questions, or it
// invalidates the prefix it lives in on every turn.
func TestSplitKeepsTheStableSideTurnIndependent(t *testing.T) {
	cb := newModeBuilder(t)
	first, _ := cb.SplitWorkspaceContextMode(context.Background(), "how do I deploy?", []string{"deploy"}, nil, "index", "")
	second, _ := cb.SplitWorkspaceContextMode(context.Background(), "what is oauth?", []string{"oauth"}, nil, "index", "")

	if first != second {
		t.Fatalf("stable half varies with the question:\n%q\nvs\n%q", first, second)
	}
	if strings.TrimSpace(first) == "" {
		t.Fatal("stable half is empty — nothing was moved into the cached prefix")
	}
}

// TestSplitFullModeStaysVolatile: full mode injects retrieval chosen from the
// query, so it must not offer a stable half at all.
func TestSplitFullModeStaysVolatile(t *testing.T) {
	cb := newModeBuilder(t)
	stable, turn := cb.SplitWorkspaceContextMode(context.Background(), "q", nil, nil, "full", "")
	if stable != "" {
		t.Fatalf("full mode must not cache a query-driven block: %q", stable)
	}
	if !strings.Contains(turn, "You are helpful") {
		t.Fatalf("full mode lost its content: %q", turn)
	}
}
