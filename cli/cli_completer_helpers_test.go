/*
 * ChatCLI - Tests for cli_completer.go pure helpers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Covers the small predicates extracted out of the legacy completer to
 * bring it under the project's cyclomatic budget:
 *   - previousToken / isFromFlag
 *   - isAtRegistryValuePosition / isAtFromFlagPosition
 *   - isPinCandidate
 *   - describeFlag / buildFlagSuggestions
 *   - contextFlagValueSuggestions
 *
 * Stateful `getXSuggestions` flows are exercised by the existing
 * integration tests; the helpers here are pure and table-friendly.
 */
package cli

import (
	"strings"
	"testing"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/pkg/persona"
)

func TestPreviousToken(t *testing.T) {
	cases := []struct {
		name string
		args []string
		line string
		want string
	}{
		{
			name: "trailing space → last full token",
			args: []string{"/skill", "install"},
			line: "/skill install ",
			want: "install",
		},
		{
			name: "mid-typing → second-to-last",
			args: []string{"/skill", "install", "fo"},
			line: "/skill install fo",
			want: "install",
		},
		{
			name: "single token, no trailing space",
			args: []string{"/skill"},
			line: "/skill",
			want: "",
		},
		{
			name: "empty args",
			args: nil,
			line: "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := previousToken(tc.args, tc.line); got != tc.want {
				t.Errorf("previousToken(%v, %q) = %q, want %q", tc.args, tc.line, got, tc.want)
			}
		})
	}
}

func TestIsFromFlag(t *testing.T) {
	cases := map[string]bool{
		"--from": true,
		"-f":     true,
		"--mode": false,
		"":       false,
		"from":   false,
	}
	for in, want := range cases {
		if got := isFromFlag(in); got != want {
			t.Errorf("isFromFlag(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsAtRegistryValuePosition(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		endsSpace bool
		want      bool
	}{
		{
			name:      "after --from with space",
			args:      []string{"/skill", "install", "my-skill", "--from"},
			endsSpace: true,
			want:      true,
		},
		{
			name:      "typing value after --from",
			args:      []string{"/skill", "install", "my-skill", "--from", "skil"},
			endsSpace: false,
			want:      true,
		},
		{
			name:      "after -f short flag",
			args:      []string{"/skill", "install", "my-skill", "-f"},
			endsSpace: true,
			want:      true,
		},
		{
			name:      "no --from yet",
			args:      []string{"/skill", "install", "my-skill"},
			endsSpace: true,
			want:      false,
		},
		{
			name: "empty args",
			args: nil,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAtRegistryValuePosition(tc.args, tc.endsSpace); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsAtFromFlagPosition(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		endsSpace bool
		want      bool
	}{
		{
			name:      "after skill name with space → flag time",
			args:      []string{"/skill", "install", "my-skill"},
			endsSpace: true,
			want:      true,
		},
		{
			name:      "typing flag mid-word",
			args:      []string{"/skill", "install", "my-skill", "--"},
			endsSpace: false,
			want:      true,
		},
		{
			name:      "still typing skill name",
			args:      []string{"/skill", "install", "my-sk"},
			endsSpace: false,
			want:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAtFromFlagPosition(tc.args, tc.endsSpace); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsPinCandidate(t *testing.T) {
	pinned := map[string]struct{}{"already-pinned": {}}
	cases := []struct {
		name  string
		skill *persona.Skill
		want  bool
	}{
		{
			name:  "nil → not candidate",
			skill: nil,
			want:  false,
		},
		{
			name:  "disabled invocation → not candidate",
			skill: &persona.Skill{Name: "manual-only", DisableModelInvocation: true},
			want:  false,
		},
		{
			name:  "already pinned → not candidate",
			skill: &persona.Skill{Name: "already-pinned"},
			want:  false,
		},
		{
			name:  "ordinary skill → candidate",
			skill: &persona.Skill{Name: "fresh"},
			want:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPinCandidate(tc.skill, pinned); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDescribeFlag_KnownFlagUsesI18n(t *testing.T) {
	// We don't assert the exact translated string (i18n catalog might
	// shift), only that the function does NOT fall through to the
	// generic "option for <cmd>" template for a flag we know is mapped.
	got := describeFlag("switch", "--mode", nil)
	if strings.Contains(got, "option for") {
		t.Errorf("expected i18n-mapped description for --mode; got %q", got)
	}
}

func TestDescribeFlag_UnknownFlagFallback(t *testing.T) {
	values := []prompt.Suggest{{Text: "a"}, {Text: "b"}}
	got := describeFlag("custom-cmd", "--banana", values)
	// Fallback path should reference the command name verbatim.
	if !strings.Contains(got, "custom-cmd") {
		t.Errorf("fallback should mention the command; got %q", got)
	}
}

func TestBuildFlagSuggestions_CoversAllFlags(t *testing.T) {
	flags := map[string][]prompt.Suggest{
		"--mode":  {{Text: "fast"}, {Text: "slow"}},
		"--model": nil,
	}
	out := buildFlagSuggestions("switch", flags)
	if len(out) != len(flags) {
		t.Fatalf("len = %d, want %d", len(out), len(flags))
	}
	seen := map[string]bool{}
	for _, s := range out {
		seen[s.Text] = true
		if s.Description == "" {
			t.Errorf("flag %q got empty description", s.Text)
		}
	}
	for f := range flags {
		if !seen[f] {
			t.Errorf("flag %q missing from output", f)
		}
	}
}

func TestContextFlagValueSuggestions(t *testing.T) {
	// `--mode` and `-m`: should produce the mode value list.
	out, matched := contextFlagValueSuggestions(
		[]string{"/context", "create", "myctx", "--mode"},
		"/context create myctx --mode ",
	)
	if !matched {
		t.Fatal("expected matched=true after --mode")
	}
	hasMode := false
	for _, s := range out {
		if s.Text == "full" || s.Text == "smart" {
			hasMode = true
		}
	}
	if !hasMode {
		t.Errorf("mode suggestions missing the canonical values; got %v", out)
	}

	// `--description`: matched=true but explicit empty suggestion list.
	out, matched = contextFlagValueSuggestions(
		[]string{"/context", "create", "myctx", "--description"},
		"/context create myctx --description ",
	)
	if !matched {
		t.Fatal("expected matched=true after --description so completer bails out")
	}
	if len(out) != 0 {
		t.Errorf("--description must NOT autocomplete paths; got %v", out)
	}

	// Unrelated previous token: matched=false → caller should keep
	// trying other strategies.
	_, matched = contextFlagValueSuggestions(
		[]string{"/context", "create", "myctx"},
		"/context create myctx ",
	)
	if matched {
		t.Errorf("non-flag previous token should not produce a match")
	}
}
