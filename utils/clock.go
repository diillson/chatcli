/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"os"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ReferenceTimeEnv pins the date ChatCLI reasons about: the "Current date"
// line the model reads, relative expressions in the user's words ("last
// week", "há 3 meses") and the recency weighting of recall. It exists so a
// conversation replayed from the past — a memory benchmark, a reproduced
// bug report — is read against the day it happened, not the day it runs.
//
// It never changes when something happens or how it is stamped: file
// times, retention, TTLs, budgets, schedules and logs keep the wall clock.
const ReferenceTimeEnv = "CHATCLI_NOW"

// referenceLayouts are the accepted spellings, most specific first. A
// value without a zone is read in the local zone, like the date line.
var referenceLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	"2006-01-02",
}

var badReferenceWarned sync.Map // value → struct{}: warn once per bad value

// ReferenceNow returns the time ChatCLI treats as "now" when it reasons
// about dates: CHATCLI_NOW when it is set and valid, the wall clock
// otherwise. A value that does not parse is logged once and ignored, so a
// typo degrades to today's date rather than to a wrong one.
func ReferenceNow() time.Time {
	raw := strings.TrimSpace(os.Getenv(ReferenceTimeEnv))
	if raw == "" {
		return time.Now()
	}
	if t, ok := ParseReferenceTime(raw); ok {
		return t
	}
	if _, seen := badReferenceWarned.LoadOrStore(raw, struct{}{}); !seen {
		zap.L().Warn("ignoring unparseable reference time; using the wall clock",
			zap.String("env", ReferenceTimeEnv), zap.String("value", raw))
	}
	return time.Now()
}

// ParseReferenceTime parses a CHATCLI_NOW value in any accepted layout.
func ParseReferenceTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range referenceLayouts {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
