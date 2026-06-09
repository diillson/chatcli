package memory

import (
	"sort"
	"testing"
)

func TestTopKSelector_SelectsHighestK(t *testing.T) {
	sel := newTopKSelector(3)
	in := map[string]float64{"a": 0.1, "b": 0.9, "c": 0.5, "d": 0.7, "e": 0.2}
	// Offer in a fixed but non-sorted order.
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		sel.offer(id, in[id])
	}
	got := sel.sortedDesc()
	if len(got) != 3 {
		t.Fatalf("want 3 items, got %d", len(got))
	}
	wantIDs := []string{"b", "d", "c"} // 0.9, 0.7, 0.5
	for i, it := range got {
		if it.id != wantIDs[i] {
			t.Fatalf("position %d: got %s (%.2f), want %s", i, it.id, it.score, wantIDs[i])
		}
	}
}

func TestTopKSelector_DeterministicTies(t *testing.T) {
	// All equal scores: output must be ascending-id, never map-order-dependent.
	build := func(order []string) []string {
		sel := newTopKSelector(3)
		for _, id := range order {
			sel.offer(id, 0.5)
		}
		out := make([]string, 0, 3)
		for _, it := range sel.sortedDesc() {
			out = append(out, it.id)
		}
		return out
	}
	a := build([]string{"x", "y", "z"})
	b := build([]string{"z", "y", "x"})
	if !sort.StringsAreSorted(a) {
		t.Fatalf("tie output not sorted: %v", a)
	}
	if len(a) != len(b) {
		t.Fatalf("nondeterministic length: %v vs %v", a, b)
	}
	// Same set of survivors regardless of insertion order (first-3 by tie rule).
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("tie ordering not insertion-independent: %v vs %v", a, b)
		}
	}
}

func TestTopKSelector_KClampAndUnderfill(t *testing.T) {
	sel := newTopKSelector(0) // clamps to 1
	sel.offer("only", 0.3)
	if got := sel.sortedDesc(); len(got) != 1 || got[0].id != "only" {
		t.Fatalf("k<=0 should clamp to 1, got %+v", got)
	}

	sel2 := newTopKSelector(10) // more capacity than items
	sel2.offer("a", 0.1)
	sel2.offer("b", 0.2)
	if got := sel2.sortedDesc(); len(got) != 2 || got[0].id != "b" {
		t.Fatalf("underfilled selector wrong: %+v", got)
	}
}
