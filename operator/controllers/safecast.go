/*
 * ChatCLI Operator - bounded numeric conversions.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Kubernetes APIs are int32-typed (replicas, ports, thresholds) while Go
 * code naturally works with int and string parameters. Bare int32(x)
 * conversions silently wrap on overflow — a malformed or malicious
 * parameter like "4294967297" would become replicas=1. These helpers make
 * every narrowing conversion explicit: parse failures surface as errors
 * and count conversions clamp at the int32 range bounds.
 */
package controllers

import (
	"math"
	"strconv"
	"strings"
)

// parseInt32 parses a decimal string into an int32, failing on values
// that do not fit in 32 bits instead of silently wrapping.
func parseInt32(s string) (int32, error) {
	v, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return 0, err
	}
	return int32(v), nil
}

// clampInt32 converts an int to int32, clamping at the int32 range
// bounds. Use for counts and sizes where saturation is the correct
// behavior on out-of-range values.
func clampInt32(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < math.MinInt32 {
		return math.MinInt32
	}
	return int32(n)
}

// leadingInt parses the leading decimal integer of s, returning 0 when s
// carries no number. It mirrors the tolerance of fmt.Sscanf("%d") for
// trailing junk ("123)" → 123) so it can replace the unchecked Sscanf
// calls used on optional labels, annotations and stack-frame fragments
// where the zero value is the documented fallback.
func leadingInt(s string) int {
	s = strings.TrimSpace(s)
	start := 0
	if start < len(s) && (s[start] == '-' || s[start] == '+') {
		start++
	}
	end := start
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == start {
		return 0
	}
	v, err := strconv.Atoi(s[:end])
	if err != nil {
		// Overflowing digit runs fall back to the zero default, same as
		// an absent value.
		return 0
	}
	return v
}
