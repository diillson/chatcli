/*
 * ChatCLI - Skill usage analytics tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package usage

import (
	"path/filepath"
	"testing"
	"time"
)

func fixedClock() func() time.Time {
	t := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func TestRecordAndRanking(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "usage.json"))
	s.now = fixedClock()

	s.Record("a", "b")
	s.Record("a")
	s.Record("a", "c")

	stats := s.Stats()
	if stats["a"].Count != 3 || stats["b"].Count != 1 || stats["c"].Count != 1 {
		t.Fatalf("counts wrong: %+v", stats)
	}
	if stats["a"].LastUsed == "" {
		t.Fatal("LastUsed not stamped")
	}

	rank := s.Ranking()
	if len(rank) != 3 || rank[0].Name != "a" || rank[0].Count != 3 {
		t.Fatalf("ranking wrong: %+v", rank)
	}
	// ties (b,c both 1) broken by name
	if rank[1].Name != "b" || rank[2].Name != "c" {
		t.Fatalf("tie-break wrong: %+v", rank)
	}
}

func TestRecordPersistsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	a := New(path)
	a.Record("x")
	a.Record("x")

	b := New(path)
	if b.Stats()["x"].Count != 2 {
		t.Fatalf("not persisted: %+v", b.Stats())
	}
}

func TestNilAndEmptySafe(t *testing.T) {
	var s *Store
	s.Record("x") // must not panic
	if s.Stats() == nil {
		t.Fatal("nil store Stats should return empty map, not nil")
	}
	if s.Ranking() != nil {
		// Ranking on nil store: Stats() returns empty map → empty slice
		if len(s.Ranking()) != 0 {
			t.Fatal("nil store ranking should be empty")
		}
	}

	ok := New(filepath.Join(t.TempDir(), "u.json"))
	ok.Record() // no names → no-op, no file
}
