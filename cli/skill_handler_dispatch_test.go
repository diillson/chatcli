/*
 * ChatCLI - Tests for SkillHandler.HandleCommand dispatch
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Covers the new pin/unpin/pinned subcommands added by the refactor, plus
 * the parser-only helpers that surround them (parseInstallArgs,
 * shortenRegistryError, riskColor).
 */
package cli

import (
	"errors"
	"testing"
)

func TestSkillHandlerHandleCommand_PinSucceeds(t *testing.T) {
	sh, _ := newPinTestFixture(t, map[string]string{
		"alpha": makeSkill("alpha", false),
	})
	if sh.IsPinned("alpha") {
		t.Fatal("pre-condition failed")
	}
	sh.HandleCommand("/skill pin alpha")
	if !sh.IsPinned("alpha") {
		t.Error("HandleCommand /skill pin alpha did not pin")
	}
}

func TestSkillHandlerHandleCommand_UnpinSucceeds(t *testing.T) {
	sh, _ := newPinTestFixture(t, map[string]string{
		"alpha": makeSkill("alpha", false),
	})
	sh.Pin("alpha")
	sh.HandleCommand("/skill unpin alpha")
	if sh.IsPinned("alpha") {
		t.Error("HandleCommand /skill unpin alpha did not unpin")
	}
}

func TestSkillHandlerHandleCommand_PinUsageError(t *testing.T) {
	// Missing argument must NOT crash and must NOT pin anything by
	// accident.
	sh, _ := newPinTestFixture(t, map[string]string{
		"alpha": makeSkill("alpha", false),
	})
	sh.HandleCommand("/skill pin")
	if len(sh.PinnedNames()) != 0 {
		t.Error("/skill pin (no arg) must not modify the pin set")
	}
}

func TestSkillHandlerHandleCommand_UnpinUsageError(t *testing.T) {
	sh, _ := newPinTestFixture(t, map[string]string{
		"alpha": makeSkill("alpha", false),
	})
	sh.Pin("alpha")
	sh.HandleCommand("/skill unpin")
	if !sh.IsPinned("alpha") {
		t.Error("/skill unpin (no arg) must leave the existing pin alone")
	}
}

func TestSkillHandlerHandleCommand_UnknownSubcommandIsNoop(t *testing.T) {
	sh, _ := newPinTestFixture(t, map[string]string{
		"alpha": makeSkill("alpha", false),
	})
	// Should print the unknown-subcommand notice and return — not crash,
	// not modify state.
	sh.HandleCommand("/skill nonexistent-subcmd")
	if len(sh.PinnedNames()) != 0 {
		t.Error("unknown subcommand must not alter pin set")
	}
}

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
	// We don't assert on the exact i18n strings (catalog changes between
	// locales) — instead we assert that each well-known input does NOT
	// reach the truncate-fallback branch and that the truncate fallback
	// IS reached for an unrecognized long message.
	cases := []struct {
		raw          string
		wantContains string // empty when we only care about "not the long input verbatim"
		wantNotEqual string
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
			// Known patterns must not echo the raw message verbatim —
			// they should map to a friendlier i18n string.
			if tc.wantContains == "" && got == tc.raw {
				t.Errorf("expected %q to map to a known-pattern message, got the raw input back", tc.raw)
			}
		})
	}
}

func TestShortenRegistryError_TruncatesLongMessages(t *testing.T) {
	long := "x" + // 64-char threshold; longer than 60 triggers the truncate branch
		"abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnop"
	got := shortenRegistryError(errors.New(long))
	if len(got) > 60 {
		t.Errorf("shortened message exceeds the 60-char ceiling: len=%d, msg=%q", len(got), got)
	}
	// Ellipsis suffix is the documented truncate marker.
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
