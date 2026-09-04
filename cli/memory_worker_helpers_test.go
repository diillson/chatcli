/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Test-only parsers that used to live in production code behind
 * //nolint:unused directives.
 */
package cli

import "strings"

// parseMemoryResponse splits the LLM response into daily notes and long-term facts.
func parseMemoryResponse(response string) (daily string, longTerm string) {
	upper := strings.ToUpper(response)

	dailyIdx := findSectionIndex(upper, "DAILY")
	longTermIdx := findSectionIndex(upper, "LONGTERM")
	if longTermIdx < 0 {
		longTermIdx = findSectionIndex(upper, "LONG-TERM")
	}
	if longTermIdx < 0 {
		longTermIdx = findSectionIndex(upper, "LONG_TERM")
	}

	extractAfter := func(idx int) string {
		nlIdx := strings.Index(response[idx:], "\n")
		if nlIdx < 0 {
			return ""
		}
		return strings.TrimSpace(response[idx+nlIdx+1:])
	}

	switch {
	case dailyIdx >= 0 && longTermIdx >= 0:
		if dailyIdx < longTermIdx {
			nlIdx := strings.Index(response[dailyIdx:], "\n")
			if nlIdx >= 0 {
				daily = strings.TrimSpace(response[dailyIdx+nlIdx+1 : longTermIdx])
			}
			longTerm = extractAfter(longTermIdx)
		} else {
			nlIdx := strings.Index(response[longTermIdx:], "\n")
			if nlIdx >= 0 {
				longTerm = strings.TrimSpace(response[longTermIdx+nlIdx+1 : dailyIdx])
			}
			daily = extractAfter(dailyIdx)
		}
	case dailyIdx >= 0:
		daily = extractAfter(dailyIdx)
	case longTermIdx >= 0:
		longTerm = extractAfter(longTermIdx)
	default:
		daily = response
	}

	if isNothingNew(daily) {
		daily = ""
	}
	if isNothingNew(longTerm) {
		longTerm = ""
	}

	return daily, longTerm
}

func findSectionIndex(upperResponse string, keyword string) int {
	patterns := []string{
		"## " + keyword,
		"##" + keyword,
		"# " + keyword,
		"**" + keyword + "**",
	}
	best := -1
	for _, p := range patterns {
		idx := strings.Index(upperResponse, p)
		if idx >= 0 && (best < 0 || idx < best) {
			best = idx
		}
	}
	return best
}
