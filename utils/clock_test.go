/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"testing"
	"time"
)

func TestParseReferenceTime(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Time
	}{
		{"2023-05-30", time.Date(2023, 5, 30, 0, 0, 0, 0, time.Local)},
		{" 2023-05-30 23:40 ", time.Date(2023, 5, 30, 23, 40, 0, 0, time.Local)},
		{"2023-05-30T23:40", time.Date(2023, 5, 30, 23, 40, 0, 0, time.Local)},
		{"2023-05-30 23:40:05", time.Date(2023, 5, 30, 23, 40, 5, 0, time.Local)},
		{"2023-05-30T23:40:05", time.Date(2023, 5, 30, 23, 40, 5, 0, time.Local)},
		{"2023-05-30T23:40:05Z", time.Date(2023, 5, 30, 23, 40, 5, 0, time.UTC)},
		{"2023-05-30T23:40:05-03:00", time.Date(2023, 5, 31, 2, 40, 5, 0, time.UTC)},
	}
	for _, c := range cases {
		got, ok := ParseReferenceTime(c.raw)
		if !ok || !got.Equal(c.want) {
			t.Errorf("ParseReferenceTime(%q) = %v, %v; want %v", c.raw, got, ok, c.want)
		}
	}
	for _, bad := range []string{"", "   ", "yesterday", "2023/05/30 (Tue) 23:40", "30-05-2023"} {
		if _, ok := ParseReferenceTime(bad); ok {
			t.Errorf("ParseReferenceTime(%q) accepted a value it should refuse", bad)
		}
	}
}

func TestReferenceNow(t *testing.T) {
	t.Run("unset follows the wall clock", func(t *testing.T) {
		t.Setenv(ReferenceTimeEnv, "")
		before := time.Now()
		got := ReferenceNow()
		if got.Before(before) || got.After(time.Now()) {
			t.Errorf("ReferenceNow() = %v, want the wall clock", got)
		}
	})
	t.Run("set pins the date", func(t *testing.T) {
		t.Setenv(ReferenceTimeEnv, "2023-05-30T23:40:00Z")
		want := time.Date(2023, 5, 30, 23, 40, 0, 0, time.UTC)
		if got := ReferenceNow(); !got.Equal(want) {
			t.Errorf("ReferenceNow() = %v, want %v", got, want)
		}
	})
	t.Run("invalid falls back to the wall clock", func(t *testing.T) {
		t.Setenv(ReferenceTimeEnv, "not-a-date")
		before := time.Now()
		// Twice: the second read must not warn again nor change the answer.
		for i := 0; i < 2; i++ {
			if got := ReferenceNow(); got.Before(before) || got.After(time.Now()) {
				t.Errorf("ReferenceNow() = %v, want the wall clock for an invalid value", got)
			}
		}
	})
}
