package cli

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/pkg/persona"
)

func ctxBg() context.Context { return context.Background() }

func TestExtractFilePaths(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "bare path with slash",
			in:   "run go test on pkg/foo/bar_test.go please",
			want: []string{"pkg/foo/bar_test.go"},
		},
		{
			name: "basename with extension",
			in:   "look at main.go for the bug",
			want: []string{"main.go"},
		},
		{
			name: "@file command",
			in:   "@file src/index.ts please",
			want: []string{"src/index.ts"},
		},
		{
			name: "@path mention",
			in:   "check @src/components/Button.tsx",
			want: []string{"src/components/Button.tsx"},
		},
		{
			name: "multiple paths dedup",
			in:   "compare pkg/a.go and pkg/b.go and pkg/a.go again",
			want: []string{"pkg/a.go", "pkg/b.go"},
		},
		{
			name: "no paths",
			in:   "hello world how are you today",
			want: nil,
		},
		{
			name: "trims surrounding punctuation",
			in:   "the file (src/x.ts) was changed.",
			want: []string{"src/x.ts"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractFilePaths(tc.in)
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("extractFilePaths(%q)\n got: %v\nwant: %v", tc.in, got, want)
			}
		})
	}
}

func TestBuildSkillInjectionBlock(t *testing.T) {
	if buildSkillInjectionBlock(nil) != "" {
		t.Fatal("nil skill slice should produce empty block")
	}
	skills := []*persona.Skill{
		{Name: "go-testing", Description: "best practices", Content: "use table tests", Version: "1.0"},
		{Name: "linting", Description: "", Content: "run golangci-lint"},
	}
	out := buildSkillInjectionBlock(skills)
	if out == "" {
		t.Fatal("expected non-empty block")
	}
	for _, sub := range []string{
		"# Auto-loaded Skills",
		"## Skill: go-testing",
		"(v1.0)",
		"best practices",
		"use table tests",
		"## Skill: linting",
		"run golangci-lint",
	} {
		if !containsString(out, sub) {
			t.Errorf("block missing %q. full:\n%s", sub, out)
		}
	}
}

func TestPickSkillModelAndEffort(t *testing.T) {
	cases := []struct {
		name         string
		skills       []*persona.Skill
		wantModel    string
		wantEffort   string
		wantConflict string
	}{
		{
			name: "single skill with both",
			skills: []*persona.Skill{
				{Name: "a", Model: "opus", Effort: "high"},
			},
			wantModel:  "opus",
			wantEffort: "high",
		},
		{
			name: "first wins for model and effort",
			skills: []*persona.Skill{
				{Name: "a", Model: "sonnet", Effort: "low"},
				{Name: "b", Model: "opus", Effort: "high"},
			},
			wantModel:    "sonnet",
			wantEffort:   "low",
			wantConflict: "b",
		},
		{
			name: "empty model falls through",
			skills: []*persona.Skill{
				{Name: "a"},
				{Name: "b", Model: "opus"},
			},
			wantModel: "opus",
		},
		{
			name:      "no skills",
			skills:    nil,
			wantModel: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model, effort, conflict := pickSkillModelAndEffort(tc.skills)
			if model != tc.wantModel {
				t.Errorf("model = %q, want %q", model, tc.wantModel)
			}
			if effort != tc.wantEffort {
				t.Errorf("effort = %q, want %q", effort, tc.wantEffort)
			}
			if conflict != tc.wantConflict {
				t.Errorf("conflict = %q, want %q", conflict, tc.wantConflict)
			}
		})
	}
}

func TestRenderManualSkillBlock(t *testing.T) {
	if renderManualSkillBlock(nil, "") != "" {
		t.Fatal("nil skill should produce empty block")
	}
	s := &persona.Skill{
		Name:        "go-testing",
		Description: "best practices",
		Content:     "write tests",
		Version:     "2.0",
	}
	out := renderManualSkillBlock(s, "run suite X")
	for _, sub := range []string{
		"# Manually Invoked Skill",
		"/go-testing",
		"(v2.0)",
		"best practices",
		"write tests",
		"### Invocation arguments",
		"run suite X",
	} {
		if !containsString(out, sub) {
			t.Errorf("manual block missing %q", sub)
		}
	}

	// No args should skip the arguments section.
	out2 := renderManualSkillBlock(s, "")
	if containsString(out2, "Invocation arguments") {
		t.Error("expected no arguments section when args is empty")
	}
}

func TestEffortRoundTripViaContext(t *testing.T) {
	ctx := client.WithEffortHint(ctxBg(), client.EffortHigh)
	if got := client.EffortFromContext(ctx); got != client.EffortHigh {
		t.Fatalf("effort round-trip failed: got %q", got)
	}
	// Unset should be a no-op.
	ctx2 := client.WithEffortHint(ctxBg(), client.EffortUnset)
	if got := client.EffortFromContext(ctx2); got != client.EffortUnset {
		t.Fatalf("unset effort leaked: got %q", got)
	}
}

func containsString(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
