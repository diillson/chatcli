/*
 * ChatCLI - Episodic memory store tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newTestEpisodeStore(t *testing.T, maxCount int) (*EpisodeStore, string) {
	t.Helper()
	dir := t.TempDir()
	return NewEpisodeStore(dir, maxCount, zap.NewNop()), dir
}

func ep(date time.Time, project, summary, outcome string, refs ...string) Episode {
	return Episode{Date: date, Project: project, Summary: summary, Outcome: outcome, Refs: refs}
}

func TestEpisodeStore_AddAndDedup(t *testing.T) {
	es, _ := newTestEpisodeStore(t, 100)
	day := time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC)

	if !es.Add(ep(day, "proj", "Fixed the OAuth loop", "shipped")) {
		t.Fatal("first add must store")
	}
	// Same day+project+summary → dedup, not a duplicate entry.
	if es.Add(ep(day.Add(2*time.Hour), "proj", "fixed  the OAuth  loop", "")) {
		t.Fatal("normalized duplicate must not add")
	}
	if es.Count() != 1 {
		t.Fatalf("expected 1 episode, got %d", es.Count())
	}
}

func TestEpisodeStore_DuplicateEnrichesOutcomeAndRefs(t *testing.T) {
	es, _ := newTestEpisodeStore(t, 100)
	day := time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC)

	es.Add(ep(day, "proj", "Built the exporter", ""))
	es.Add(ep(day, "proj", "Built the exporter", "merged as PR 12", "exporter.go"))

	eps := es.Range(time.Time{}, time.Time{}, "", "", 0)
	if len(eps) != 1 {
		t.Fatalf("expected 1 episode, got %d", len(eps))
	}
	if eps[0].Outcome != "merged as PR 12" {
		t.Errorf("outcome not enriched: %q", eps[0].Outcome)
	}
	if len(eps[0].Refs) != 1 || eps[0].Refs[0] != "exporter.go" {
		t.Errorf("refs not enriched: %v", eps[0].Refs)
	}
}

func TestEpisodeStore_RangeFilters(t *testing.T) {
	es, _ := newTestEpisodeStore(t, 100)
	april := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)
	may := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	es.Add(ep(april, "/home/u/alpha", "Migrated the schema", "done"))
	es.Add(ep(may, "/home/u/beta", "Added retry logic", "flaky test remains", "retry.go"))

	if got := es.Range(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), "", "", 0); len(got) != 1 || got[0].Summary != "Migrated the schema" {
		t.Errorf("date window filter failed: %v", got)
	}
	if got := es.Range(time.Time{}, time.Time{}, "beta", "", 0); len(got) != 1 || got[0].Summary != "Added retry logic" {
		t.Errorf("project filter failed: %v", got)
	}
	if got := es.Range(time.Time{}, time.Time{}, "", "retry logic", 0); len(got) != 1 || got[0].Summary != "Added retry logic" {
		t.Errorf("query filter failed: %v", got)
	}
	if got := es.Range(time.Time{}, time.Time{}, "", "retry schema", 0); len(got) != 0 {
		t.Errorf("AND semantics should reject partial matches: %v", got)
	}
}

func TestEpisodeStore_LimitKeepsMostRecent(t *testing.T) {
	es, _ := newTestEpisodeStore(t, 100)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		es.Add(ep(base.AddDate(0, 0, i), "p", "work item "+string(rune('a'+i)), ""))
	}
	got := es.Range(time.Time{}, time.Time{}, "", "", 2)
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
	if got[0].Summary != "work item d" || got[1].Summary != "work item e" {
		t.Errorf("limit must keep the most recent, chronological: %v, %v", got[0].Summary, got[1].Summary)
	}
}

func TestEpisodeStore_PersistenceRoundtrip(t *testing.T) {
	es, dir := newTestEpisodeStore(t, 100)
	day := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	es.Add(ep(day, "proj", "Wrote the parser", "green tests", "parser.go"))

	reloaded := NewEpisodeStore(dir, 100, zap.NewNop())
	eps := reloaded.Range(time.Time{}, time.Time{}, "", "", 0)
	if len(eps) != 1 || eps[0].Summary != "Wrote the parser" || eps[0].Outcome != "green tests" {
		t.Fatalf("roundtrip lost data: %+v", eps)
	}
}

func TestEpisodeStore_MultiProcessMerge(t *testing.T) {
	dir := t.TempDir()
	a := NewEpisodeStore(dir, 100, zap.NewNop())
	b := NewEpisodeStore(dir, 100, zap.NewNop())

	a.Add(ep(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), "p", "process A work", ""))
	b.Add(ep(time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC), "p", "process B work", ""))
	// A persists again — must adopt B's episode instead of clobbering it.
	a.Add(ep(time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC), "p", "more A work", ""))

	fresh := NewEpisodeStore(dir, 100, zap.NewNop())
	if n := fresh.Count(); n != 3 {
		t.Fatalf("expected union of 3 episodes on disk, got %d", n)
	}
}

func TestEpisodeStore_CapDropsOldest(t *testing.T) {
	es, _ := newTestEpisodeStore(t, 3)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		es.Add(ep(base.AddDate(0, 0, i), "p", "entry "+string(rune('a'+i)), ""))
	}
	eps := es.Range(time.Time{}, time.Time{}, "", "", 0)
	if len(eps) != 3 {
		t.Fatalf("cap not applied: %d", len(eps))
	}
	if eps[0].Summary != "entry c" {
		t.Errorf("oldest must be dropped first, kept %q", eps[0].Summary)
	}
}

func TestEpisodeStore_QuarantinesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "episodes.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	es := NewEpisodeStore(dir, 100, zap.NewNop())
	if es.Count() != 0 {
		t.Fatal("corrupt store must start empty")
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Error("corrupt file must be quarantined aside, not left in place")
	}
}

func TestFormatEpisodes(t *testing.T) {
	day := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	out := FormatEpisodes([]*Episode{
		{Date: day, Project: "chatcli", Summary: "Fixed the loop", Outcome: "shipped", Refs: []string{"a.go"}},
	})
	for _, want := range []string{"2026-04-10", "[chatcli]", "Fixed the loop", "shipped", "a.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatted output missing %q: %s", want, out)
		}
	}
	if FormatEpisodes(nil) != "" {
		t.Error("empty input must format to empty string")
	}
}
