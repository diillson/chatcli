/*
 * ChatCLI - Temporal query parsing tests (EN + PT).
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package memory

import (
	"strings"
	"testing"
	"time"
)

// now is fixed so every assertion is deterministic: Friday 2026-07-17.
var temporalNow = time.Date(2026, 7, 17, 15, 30, 0, 0, time.UTC)

func TestParseTemporalRange_RelativeMonthsEN(t *testing.T) {
	from, to, cleaned, ok := ParseTemporalRange("what did we do 3 months ago on auth", temporalNow)
	if !ok {
		t.Fatal("expected temporal hit")
	}
	if from.Month() != time.April || from.Day() != 1 || from.Year() != 2026 {
		t.Errorf("expected April 2026 start, got %v", from)
	}
	if to.Month() != time.May {
		t.Errorf("expected May bound, got %v", to)
	}
	if !strings.Contains(cleaned, "auth") || strings.Contains(cleaned, "months") {
		t.Errorf("cleaned query wrong: %q", cleaned)
	}
}

func TestParseTemporalRange_RelativePT(t *testing.T) {
	cases := []struct {
		q         string
		wantMonth time.Month
	}{
		{"o que fizemos há 3 meses", time.April},
		{"o que fizemos 3 meses atrás", time.April},
		{"ha 2 meses trabalhamos no gateway", time.May},
	}
	for _, c := range cases {
		from, _, _, ok := ParseTemporalRange(c.q, temporalNow)
		if !ok {
			t.Errorf("%q: expected temporal hit", c.q)
			continue
		}
		if from.Month() != c.wantMonth {
			t.Errorf("%q: expected %v, got %v", c.q, c.wantMonth, from.Month())
		}
	}
}

func TestParseTemporalRange_WeeksAndDays(t *testing.T) {
	from, to, _, ok := ParseTemporalRange("2 weeks ago", temporalNow)
	if !ok {
		t.Fatal("expected hit")
	}
	// 2026-07-17 is a Friday; current week starts Mon 2026-07-13.
	wantMonday := time.Date(2026, 6, 29, 0, 0, 0, 0, time.UTC)
	if !from.Equal(wantMonday) {
		t.Errorf("expected week start %v, got %v", wantMonday, from)
	}
	if to.Sub(from) != 7*24*time.Hour {
		t.Errorf("expected 7-day window, got %v", to.Sub(from))
	}

	from, to, _, ok = ParseTemporalRange("há 3 dias", temporalNow)
	if !ok {
		t.Fatal("expected hit")
	}
	wantDay := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	if !from.Equal(wantDay) || to.Sub(from) != 24*time.Hour {
		t.Errorf("expected single day 2026-07-14, got %v..%v", from, to)
	}
}

func TestParseTemporalRange_NamedPhrases(t *testing.T) {
	from, _, _, ok := ParseTemporalRange("mês passado", temporalNow)
	if !ok || from.Month() != time.June {
		t.Errorf("mês passado: expected June, ok=%v from=%v", ok, from)
	}
	from, _, _, ok = ParseTemporalRange("last week", temporalNow)
	if !ok || !from.Equal(time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("last week: expected Mon 2026-07-06, ok=%v from=%v", ok, from)
	}
	from, to, _, ok := ParseTemporalRange("ontem", temporalNow)
	if !ok || from.Day() != 16 || to.Day() != 17 {
		t.Errorf("ontem: expected 2026-07-16 day window, ok=%v from=%v to=%v", ok, from, to)
	}
}

func TestParseTemporalRange_MonthNames(t *testing.T) {
	// April is in the past of July → current year.
	from, _, cleaned, ok := ParseTemporalRange("o que rolou em abril no chatcli", temporalNow)
	if !ok || from.Month() != time.April || from.Year() != 2026 {
		t.Fatalf("abril: ok=%v from=%v", ok, from)
	}
	if !strings.Contains(cleaned, "chatcli") || strings.Contains(strings.ToLower(cleaned), "abril") {
		t.Errorf("cleaned wrong: %q", cleaned)
	}
	// December is ahead of July → previous year.
	from, _, _, ok = ParseTemporalRange("december deploy", temporalNow)
	if !ok || from.Year() != 2025 || from.Month() != time.December {
		t.Errorf("december: expected Dec 2025, ok=%v from=%v", ok, from)
	}
	// Explicit year wins.
	from, _, _, ok = ParseTemporalRange("abril de 2025", temporalNow)
	if !ok || from.Year() != 2025 || from.Month() != time.April {
		t.Errorf("abril de 2025: ok=%v from=%v", ok, from)
	}
}

func TestParseTemporalRange_ISO(t *testing.T) {
	from, to, _, ok := ParseTemporalRange("2026-04", temporalNow)
	if !ok || from.Month() != time.April || to.Month() != time.May {
		t.Errorf("ISO month: ok=%v from=%v to=%v", ok, from, to)
	}
	from, to, _, ok = ParseTemporalRange("bug do dia 2026-04-12", temporalNow)
	if !ok || from.Day() != 12 || to.Sub(from) != 24*time.Hour {
		t.Errorf("ISO day: ok=%v from=%v to=%v", ok, from, to)
	}
}

func TestParseTemporalRange_NoTemporalContent(t *testing.T) {
	_, _, cleaned, ok := ParseTemporalRange("how does the cache planner work", temporalNow)
	if ok {
		t.Error("plain content query must not produce a window")
	}
	if cleaned != "how does the cache planner work" {
		t.Errorf("query must pass through untouched: %q", cleaned)
	}
	if _, _, _, ok := ParseTemporalRange("", temporalNow); ok {
		t.Error("empty query must not hit")
	}
}
