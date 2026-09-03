/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestEpisodeStore_SearchRanksByRelevance(t *testing.T) {
	es := NewEpisodeStore(t.TempDir(), 100, zap.NewNop())
	day := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	add := func(offset int, project, summary, outcome string) {
		if !es.Add(Episode{Date: day.AddDate(0, 0, offset), Project: project, Summary: summary, Outcome: outcome}) {
			t.Fatalf("add %q", summary)
		}
	}
	add(0, "billing", "migrated invoices table to partitioned layout", "cut query time in half")
	add(1, "billing", "fixed rounding bug in tax calculation", "green tests")
	add(2, "gateway", "rolled out telegram gateway with hub isolation", "shipped")
	add(3, "docs", "rewrote onboarding guide", "published")

	hits := es.Search("tax rounding bug", 2)
	if len(hits) == 0 || hits[0].Summary != "fixed rounding bug in tax calculation" {
		t.Fatalf("BM25 must put the tax episode first, got %+v", summaries(hits))
	}
	if got := es.Search("telegram", 5); len(got) != 1 || got[0].Project != "gateway" {
		t.Fatalf("episodes sharing no term must be absent, got %v", summaries(got))
	}
	if got := es.Search("", 5); got != nil {
		t.Fatal("empty query returns nothing")
	}

	// SearchWithin honors the window before ranking.
	within := es.SearchWithin(day.AddDate(0, 0, 2), time.Time{}, "", "telegram gateway rounding", 5)
	if len(within) != 1 || within[0].Project != "gateway" {
		t.Fatalf("window must exclude the earlier billing episode, got %v", summaries(within))
	}
}

func summaries(eps []*Episode) []string {
	out := make([]string, len(eps))
	for i, e := range eps {
		out[i] = e.Summary
	}
	return out
}
