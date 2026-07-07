/*
 * ChatCLI - Persona System
 * Tests for Skill frontmatter auto-activation matchers.
 */
package persona

import "testing"

func TestMatchesTrigger(t *testing.T) {
	s := &Skill{Triggers: StringList{"test", "TDD", "unit-tests"}}
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"exact lowercase", "please write tests for X", true},
		{"case-insensitive", "I want TDD here", true},
		{"case-insensitive 2", "writing Tdd now", true},
		{"compound word", "how to run unit-tests in CI", true},
		{"no match", "refactor this module", false},
		{"empty input", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.MatchesTrigger(tc.in); got != tc.want {
				t.Fatalf("MatchesTrigger(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}

	empty := &Skill{}
	if empty.MatchesTrigger("anything") {
		t.Fatal("skill with no triggers should not match")
	}
}

func TestMatchesTrigger_MultiWordAnyOrder(t *testing.T) {
	s := &Skill{Triggers: StringList{"servicenow login"}}
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"contiguous phrase (fast path)", "faz o servicenow login pra mim", true},
		{"reversed order with words between", "preciso fazer o login no servicenow", true},
		{"reversed order, case-insensitive", "como faço Login no ServiceNow?", true},
		{"words split across the sentence", "o login da conta corporativa fica no portal servicenow", true},
		{"only one word present", "preciso acessar o servicenow", false},
		{"other word only", "esqueci meu login do gmail", false},
		{"neither word", "abre um chamado pra mim", false},
		{"empty input", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.MatchesTrigger(tc.in); got != tc.want {
				t.Fatalf("MatchesTrigger(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}

	// Single-word triggers keep pure substring semantics — no regression.
	single := &Skill{Triggers: StringList{"servicenow"}}
	if !single.MatchesTrigger("acessa servicenow.com aí") {
		t.Fatal("single-word trigger must keep substring matching")
	}
}

// Multi-word tier must match on WHOLE tokens: short trigger words ("me",
// "on", "no") appearing inside other words must not fire. This is the exact
// false positive that a per-word substring check produces for the builtin
// send-message skill (trigger "message me" vs input "unrelated user message",
// where "me" hides inside "message").
func TestMatchesTrigger_MultiWordNeedsWholeTokens(t *testing.T) {
	s := &Skill{Triggers: StringList{"message me"}}
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"short word hidden inside another word", "unrelated user message", false},
		{"both as whole tokens, any order", "can you me... send that message?", true},
		{"contiguous phrase still works", "please message me when done", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.MatchesTrigger(tc.in); got != tc.want {
				t.Fatalf("MatchesTrigger(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}

	// Punctuation inside trigger words tokenizes the same way as the text,
	// so "run unit-tests" fires on "unit-tests run fine" (order-free).
	hyphen := &Skill{Triggers: StringList{"run unit-tests"}}
	if !hyphen.MatchesTrigger("the unit-tests only run in CI") {
		t.Fatal("hyphenated trigger words must tokenize like the text")
	}
	if hyphen.MatchesTrigger("run the linter") {
		t.Fatal("missing trigger tokens must not match")
	}
}

func TestMatchesPath_Basic(t *testing.T) {
	s := &Skill{Paths: StringList{"*_test.go"}}
	cases := []struct {
		name string
		in   []string
		want bool
	}{
		{"basename match", []string{"foo_test.go"}, true},
		{"deep basename match", []string{"pkg/foo/bar_test.go"}, true},
		{"no match", []string{"pkg/foo/bar.go"}, false},
		{"empty", []string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.MatchesPath(tc.in); got != tc.want {
				t.Fatalf("MatchesPath(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestMatchesPath_DoubleStar(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		// Leading **
		{"**/*.go", "main.go", true},
		{"**/*.go", "pkg/foo/bar.go", true},
		{"**/*.go", "README.md", false},

		// Trailing **
		{"test/**", "test/unit/a.go", true},
		{"test/**", "test/a.go", true},
		{"test/**", "src/a.go", false},

		// Middle **
		{"src/**/*.ts", "src/a.ts", true},
		{"src/**/*.ts", "src/components/Button.ts", true},
		{"src/**/*.ts", "src/pages/dashboard/index.ts", true},
		{"src/**/*.ts", "lib/foo.ts", false},

		// Simple * within segment
		{"src/*.ts", "src/a.ts", true},
		{"src/*.ts", "src/components/a.ts", false},

		// Question mark
		{"?ain.go", "main.go", true},
		{"?ain.go", "brain.go", false},

		// Collapsed doublestars
		{"**/**/a.txt", "x/y/a.txt", true},
		{"**/**/a.txt", "a.txt", true},
	}
	for _, tc := range cases {
		t.Run(tc.pattern+"|"+tc.path, func(t *testing.T) {
			s := &Skill{Paths: StringList{tc.pattern}}
			if got := s.MatchesPath([]string{tc.path}); got != tc.want {
				t.Fatalf("MatchesPath(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

func TestMatchesPath_Normalization(t *testing.T) {
	s := &Skill{Paths: StringList{"src/**/*.ts"}}

	// Windows-style backslashes in input paths.
	if !s.MatchesPath([]string{"src\\components\\Button.ts"}) {
		t.Fatal("expected backslash-normalized match")
	}

	// Backslashes in pattern.
	s2 := &Skill{Paths: StringList{"src\\**\\*.ts"}}
	if !s2.MatchesPath([]string{"src/components/Button.ts"}) {
		t.Fatal("expected pattern-with-backslashes to match forward-slash path")
	}
}

func TestMatchesPath_MultiplePatterns(t *testing.T) {
	s := &Skill{Paths: StringList{"*.md", "src/**/*.ts"}}

	if !s.MatchesPath([]string{"README.md"}) {
		t.Fatal("should match first pattern")
	}
	if !s.MatchesPath([]string{"src/x/y/z.ts"}) {
		t.Fatal("should match second pattern")
	}
	if s.MatchesPath([]string{"cmd/main.go"}) {
		t.Fatal("should not match any pattern")
	}
}
