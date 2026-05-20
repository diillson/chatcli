/*
 * ChatCLI - Chat envelope rendering tests (PR4)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
)

// TestFormatLatency locks the human-friendly latency formatter that
// powers the chat envelope's right-hand metric. Sub-second values land
// in ms, anything bigger uses seconds with one decimal — keeps the
// header column-width predictable.
func TestFormatLatency(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0ms"},
		{250 * time.Millisecond, "250ms"},
		{999 * time.Millisecond, "999ms"},
		{1 * time.Second, "1.0s"},
		{1400 * time.Millisecond, "1.4s"},
		{12345 * time.Millisecond, "12.3s"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, formatLatency(tc.in), "duration=%v", tc.in)
	}
}

// TestFormatTokenSummary covers all three branches of the token
// summary helper: nil usage, zero counts, and real values. The
// fallback to the i18n placeholder is the user-visible signal that
// the provider didn't return token counts.
func TestFormatTokenSummary(t *testing.T) {
	// nil usage → placeholder
	assert.Equal(t, "—", formatTokenSummary(nil))
	// zero counts → placeholder
	assert.Equal(t, "—", formatTokenSummary(&models.UsageInfo{}))
	// Real counts → formatted via i18n.T which uses golang.org/x/text/message
	// for locale-aware number formatting. The exact separator (comma vs
	// dot vs none) depends on the active locale, so we assert the arrows
	// are present and the digits survive — without locking the separator.
	out := formatTokenSummary(&models.UsageInfo{PromptTokens: 312, CompletionTokens: 1800})
	assert.Contains(t, out, "312")
	// 1800 may render as "1,800" (en) / "1.800" (pt) / "1800" — assert
	// on the digit prefix instead of the exact form.
	assert.True(t, containsAnyDigits(out, "1800", "1,800", "1.800"),
		"token summary must include 1800 in some locale form, got %q", out)
	assert.Contains(t, out, "↑")
	assert.Contains(t, out, "↓")
}

// containsAnyDigits returns true if any of the variants appears in s.
func containsAnyDigits(s string, variants ...string) bool {
	for _, v := range variants {
		if assertContains(s, v) {
			return true
		}
	}
	return false
}

func assertContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
