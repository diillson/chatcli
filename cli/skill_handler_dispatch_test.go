/*
 * ChatCLI - Tests for SkillHandler parser-only helpers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Covers the pure helpers that surround the SkillHandler dispatch:
 *   - parseInstallArgs
 *   - shortenRegistryError
 *   - riskColor
 */
package cli

import (
	"errors"
	"testing"
)

func TestParseInstallArgs_Variations(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantSkill string
		wantFrom  string
	}{
		{name: "empty args", args: nil, wantSkill: "", wantFrom: ""},
		{
			name:      "name only",
			args:      []string{"frontend-design"},
			wantSkill: "frontend-design",
		},
		{
			name:      "name plus --from value",
			args:      []string{"frontend-design", "--from", "skills.sh"},
			wantSkill: "frontend-design",
			wantFrom:  "skills.sh",
		},
		{
			name:      "name plus short -f",
			args:      []string{"frontend-design", "-f", "local"},
			wantSkill: "frontend-design",
			wantFrom:  "local",
		},
		{
			name:      "dangling --from has no value",
			args:      []string{"frontend-design", "--from"},
			wantSkill: "frontend-design",
			wantFrom:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			skill, from := parseInstallArgs(tc.args)
			if skill != tc.wantSkill {
				t.Errorf("skill = %q, want %q", skill, tc.wantSkill)
			}
			if from != tc.wantFrom {
				t.Errorf("from = %q, want %q", from, tc.wantFrom)
			}
		})
	}
}

func TestShortenRegistryError_PatternMapping(t *testing.T) {
	cases := []struct {
		raw          string
		wantContains string
	}{
		{raw: "no such host: foo.bar"},
		{raw: "connection refused"},
		{raw: "context deadline exceeded"},
		{raw: "x509: certificate signed by unknown authority"},
		{raw: "not_found_error: missing"},
		{raw: "short", wantContains: "short"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got := shortenRegistryError(errors.New(tc.raw))
			if tc.wantContains != "" && got != tc.wantContains {
				t.Errorf("got %q, want exactly %q", got, tc.wantContains)
			}
			if tc.wantContains == "" && got == tc.raw {
				t.Errorf("expected %q to map to a known-pattern message, got the raw input back", tc.raw)
			}
		})
	}
}

func TestShortenRegistryError_TruncatesLongMessages(t *testing.T) {
	long := "x" +
		"abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnop"
	got := shortenRegistryError(errors.New(long))
	if len(got) > 60 {
		t.Errorf("shortened message exceeds the 60-char ceiling: len=%d, msg=%q", len(got), got)
	}
	if got[len(got)-3:] != "..." {
		t.Errorf("expected trailing ellipsis on truncated message; got %q", got)
	}
}

func TestRiskColor_KnownLevelsMapToCorrectColors(t *testing.T) {
	cases := map[string]string{
		"safe":     ColorGreen,
		"SAFE":     ColorGreen,
		"low":      ColorGreen,
		"medium":   ColorYellow,
		"high":     ColorRed,
		"critical": ColorRed,
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := riskColor(in); got != want {
				t.Errorf("riskColor(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

func TestRiskColor_UnknownLevelsFallToGray(t *testing.T) {
	for _, in := range []string{"", "weird", "unknown", "totally-made-up"} {
		if got := riskColor(in); got != ColorGray {
			t.Errorf("riskColor(%q) = %q, want ColorGray", in, got)
		}
	}
}
