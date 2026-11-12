/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * ChatCLI - Scheduler: tolerant duration parsing.
 *
 * Every duration a person or a model types for the scheduler — a poll
 * interval, a wait timeout, a relative fire time — goes through
 * ParseLooseDuration. It accepts everything time.ParseDuration does plus
 * the spellings that show up in practice ("5 min", "2 hours", "1d",
 * "30 segundos"), and it rejects what it cannot read. A value that failed
 * to parse used to be dropped in silence, leaving the job on a default
 * the caller never asked for.
 */
package scheduler

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// looseDurationUnits maps the accepted unit spellings to their length.
var looseDurationUnits = map[string]time.Duration{
	"ms": time.Millisecond, "msec": time.Millisecond, "msecs": time.Millisecond,
	"millisecond": time.Millisecond, "milliseconds": time.Millisecond,
	"s": time.Second, "sec": time.Second, "secs": time.Second,
	"second": time.Second, "seconds": time.Second,
	"seg": time.Second, "segs": time.Second, "segundo": time.Second, "segundos": time.Second,
	"m": time.Minute, "min": time.Minute, "mins": time.Minute,
	"minute": time.Minute, "minutes": time.Minute, "minuto": time.Minute, "minutos": time.Minute,
	"h": time.Hour, "hr": time.Hour, "hrs": time.Hour,
	"hour": time.Hour, "hours": time.Hour, "hora": time.Hour, "horas": time.Hour,
	"d": 24 * time.Hour, "day": 24 * time.Hour, "days": 24 * time.Hour,
	"dia": 24 * time.Hour, "dias": 24 * time.Hour,
	"w": 7 * 24 * time.Hour, "wk": 7 * 24 * time.Hour, "wks": 7 * 24 * time.Hour,
	"week": 7 * 24 * time.Hour, "weeks": 7 * 24 * time.Hour,
	"semana": 7 * 24 * time.Hour, "semanas": 7 * 24 * time.Hour,
}

// looseDurationTerm is one "<number><unit>" piece of a duration.
var looseDurationTerm = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([a-z]+)`)

// ParseLooseDuration reads a duration written the way people write one.
// The result is never negative; a bare number is rejected because the unit
// is what tells 30 seconds from 30 minutes.
func ParseLooseDuration(input string) (time.Duration, error) {
	in := strings.ToLower(strings.TrimSpace(input))
	if in == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if d, err := time.ParseDuration(strings.ReplaceAll(in, " ", "")); err == nil {
		if d < 0 {
			return 0, fmt.Errorf("duration %q is negative", input)
		}
		return d, nil
	}
	if _, err := strconv.ParseFloat(in, 64); err == nil {
		return 0, fmt.Errorf("duration %q needs a unit (for example %ss or %sm)", input, in, in)
	}
	var total time.Duration
	rest := in
	for rest != "" {
		m := looseDurationTerm.FindStringSubmatch(rest)
		if m == nil {
			return 0, fmt.Errorf("cannot read duration %q (examples: 30s, 5m, 2h, 1d, \"10 min\")", input)
		}
		unit, ok := looseDurationUnits[m[2]]
		if !ok {
			return 0, fmt.Errorf("duration %q has an unknown unit %q (use ms, s, m, h, d or w)", input, m[2])
		}
		n, _ := strconv.ParseFloat(m[1], 64)
		total += time.Duration(n * float64(unit))
		rest = strings.TrimLeft(rest[len(m[0]):], " ,")
		rest = strings.TrimPrefix(rest, "and ")
		rest = strings.TrimPrefix(rest, "e ")
	}
	return total, nil
}
