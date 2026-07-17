/*
 * ChatCLI - Long-term memory: temporal query parsing.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Turns the time expressions people actually type — "3 months ago", "há 3
 * meses", "em abril", "last week", "2026-04" — into a concrete [from, to)
 * window so recall and the timeline can answer "what did we do back then?".
 * English and Portuguese are covered because those are the product's two
 * locales; anything unrecognized simply reports ok=false and the caller
 * falls back to relevance-only retrieval.
 */
package memory

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// relativeAgoRe matches "3 months ago", "2 weeks ago", "10 days ago" and the
// Portuguese forms "há 3 meses" / "ha 3 meses" / "3 meses atrás" / "3 meses atras".
var relativeAgoRe = regexp.MustCompile(
	`(?i)\b(?:(?:h[áa]\s+)?(\d{1,3})\s+(day|days|dia|dias|week|weeks|semana|semanas|month|months|m[êe]s|meses)(?:\s+(?:ago|atr[áa]s))?)\b`)

// isoMonthRe matches "2026-04"; isoDayRe matches "2026-04-12";
// monthYearSuffixRe matches the optional " de 2026" after a month name.
var (
	isoDayRe          = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)
	isoMonthRe        = regexp.MustCompile(`\b(\d{4})-(\d{2})\b`)
	monthYearSuffixRe = regexp.MustCompile(`^(?:\s+(?:de|of))?\s+(\d{4})\b`)
)

// monthNames maps English and Portuguese month names (accented and plain) to
// their month number.
var monthNames = map[string]time.Month{
	"january": time.January, "february": time.February, "march": time.March,
	"april": time.April, "may": time.May, "june": time.June, "july": time.July,
	"august": time.August, "september": time.September, "october": time.October,
	"november": time.November, "december": time.December,
	"janeiro": time.January, "fevereiro": time.February, "março": time.March,
	"marco": time.March, "abril": time.April, "maio": time.May, "junho": time.June,
	"julho": time.July, "agosto": time.August, "setembro": time.September,
	"outubro": time.October, "novembro": time.November, "dezembro": time.December,
}

// namedRanges are fixed phrases resolved relative to now.
var namedRanges = []struct {
	phrase string
	rangeF func(now time.Time) (time.Time, time.Time)
}{
	{"last week", weekOffset(-1)}, {"semana passada", weekOffset(-1)},
	{"this week", weekOffset(0)}, {"esta semana", weekOffset(0)}, {"essa semana", weekOffset(0)},
	{"last month", monthOffset(-1)}, {"mês passado", monthOffset(-1)}, {"mes passado", monthOffset(-1)},
	{"this month", monthOffset(0)}, {"este mês", monthOffset(0)}, {"este mes", monthOffset(0)}, {"esse mês", monthOffset(0)}, {"esse mes", monthOffset(0)},
	{"yesterday", dayOffset(-1)}, {"ontem", dayOffset(-1)},
	{"today", dayOffset(0)}, {"hoje", dayOffset(0)},
}

// ParseTemporalRange extracts a time window from a natural-language query.
// It returns the [from, to) window, the query with the temporal expression
// removed (so the remaining words can drive content matching), and whether a
// window was recognized at all.
func ParseTemporalRange(query string, now time.Time) (from, to time.Time, cleaned string, ok bool) {
	q := strings.TrimSpace(query)
	if q == "" {
		return time.Time{}, time.Time{}, query, false
	}
	lower := strings.ToLower(q)

	// Exact dates first — the most specific signal wins.
	if m := isoDayRe.FindStringSubmatch(lower); m != nil {
		if day, err := time.ParseInLocation("2006-01-02", m[0], now.Location()); err == nil {
			return day, day.AddDate(0, 0, 1), removeSpan(q, lower, m[0]), true
		}
	}
	if m := isoMonthRe.FindStringSubmatch(lower); m != nil {
		year, _ := strconv.Atoi(m[1])
		monthNum, _ := strconv.Atoi(m[2])
		if monthNum >= 1 && monthNum <= 12 {
			start := time.Date(year, time.Month(monthNum), 1, 0, 0, 0, 0, now.Location())
			return start, start.AddDate(0, 1, 0), removeSpan(q, lower, m[0]), true
		}
	}

	// Relative: "3 months ago" / "há 3 semanas" / "10 dias atrás".
	if m := relativeAgoRe.FindStringSubmatch(lower); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n > 0 {
			unit := m[2]
			var start, end time.Time
			switch {
			case strings.HasPrefix(unit, "day") || strings.HasPrefix(unit, "dia"):
				start, end = dayOffset(-n)(now)
			case strings.HasPrefix(unit, "week") || strings.HasPrefix(unit, "semana"):
				start, end = weekOffset(-n)(now)
			default: // month | mês | mes | meses
				start, end = monthOffset(-n)(now)
			}
			return start, end, removeSpan(q, lower, m[0]), true
		}
	}

	// Named phrases: "last week", "mês passado", "ontem", …
	for _, nr := range namedRanges {
		if idx := strings.Index(lower, nr.phrase); idx >= 0 {
			start, end := nr.rangeF(now)
			return start, end, removeSpan(q, lower, nr.phrase), true
		}
	}

	// Bare month names: "april", "em abril", "abril de 2026". A month later
	// than the current one means the most recent PAST occurrence (last year).
	for _, word := range strings.Fields(lower) {
		w := strings.Trim(word, ".,;:!?")
		month, isMonth := monthNames[w]
		if !isMonth {
			continue
		}
		year := now.Year()
		rest := lower[strings.Index(lower, w)+len(w):]
		ym := monthYearSuffixRe.FindStringSubmatch(rest)
		// "may" doubles as an English modal verb ("what may have caused…"),
		// so as a month it only counts with an explicit year. The Portuguese
		// "maio" is unambiguous and needs no such guard.
		if w == "may" && ym == nil {
			continue
		}
		if ym != nil {
			year, _ = strconv.Atoi(ym[1])
		} else if month > now.Month() {
			year--
		}
		start := time.Date(year, month, 1, 0, 0, 0, 0, now.Location())
		return start, start.AddDate(0, 1, 0), removeSpan(q, lower, w), true
	}

	return time.Time{}, time.Time{}, query, false
}

// removeSpan deletes the recognized temporal expression (and adjacent
// connective words) from the original-case query, collapsing whitespace so
// what remains is a clean content query.
func removeSpan(original, lower, span string) string {
	idx := strings.Index(lower, span)
	if idx < 0 {
		return original
	}
	out := original[:idx] + original[idx+len(span):]
	// Strip dangling connectives left behind ("in", "em", "de", "no", "na").
	fields := strings.Fields(out)
	cleaned := make([]string, 0, len(fields))
	for _, f := range fields {
		switch strings.ToLower(strings.Trim(f, ".,;:!?")) {
		case "in", "on", "em", "de", "do", "da", "no", "na":
			continue
		}
		cleaned = append(cleaned, f)
	}
	return strings.Join(cleaned, " ")
}

// dayOffset returns the [start, end) of the day n days from today.
func dayOffset(n int) func(time.Time) (time.Time, time.Time) {
	return func(now time.Time) (time.Time, time.Time) {
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, n)
		return day, day.AddDate(0, 0, 1)
	}
}

// weekOffset returns the [monday, monday+7d) of the week n weeks from the
// current one.
func weekOffset(n int) func(time.Time) (time.Time, time.Time) {
	return func(now time.Time) (time.Time, time.Time) {
		weekday := int(now.Weekday())
		if weekday == 0 { // Sunday closes the week, it doesn't open the next
			weekday = 7
		}
		monday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).
			AddDate(0, 0, -(weekday-1)).
			AddDate(0, 0, 7*n)
		return monday, monday.AddDate(0, 0, 7)
	}
}

// monthOffset returns the [first, first+1mo) of the month n months from the
// current one.
func monthOffset(n int) func(time.Time) (time.Time, time.Time) {
	return func(now time.Time) (time.Time, time.Time) {
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, n, 0)
		return first, first.AddDate(0, 1, 0)
	}
}
