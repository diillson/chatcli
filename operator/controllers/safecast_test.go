package controllers

import (
	"math"
	"testing"
)

func TestParseInt32(t *testing.T) {
	cases := []struct {
		in      string
		want    int32
		wantErr bool
	}{
		{"0", 0, false},
		{"3", 3, false},
		{"-12", -12, false},
		{"2147483647", math.MaxInt32, false},
		{"-2147483648", math.MinInt32, false},
		{"2147483648", 0, true},  // one past MaxInt32 → error, not wrap
		{"-2147483649", 0, true}, // one past MinInt32 → error, not wrap
		{"4294967297", 0, true},  // would silently become 1 with int32(Atoi)
		{"not-a-number", 0, true},
		{"", 0, true},
	}
	for _, tc := range cases {
		got, err := parseInt32(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseInt32(%q) expected error, got %d", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parseInt32(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
}

func TestLeadingInt(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"42", 42},
		{"  42  ", 42},
		{"123)", 123},    // Sscanf-style tolerance for trailing junk
		{"123:456", 123}, // stack-frame fragment
		{"-7", -7},
		{"+9", 9},
		{"", 0},
		{"abc", 0},
		{"-", 0},
		{"99999999999999999999", 0}, // overflow → zero fallback
	}
	for _, tc := range cases {
		if got := leadingInt(tc.in); got != tc.want {
			t.Errorf("leadingInt(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestValidateGitInputs(t *testing.T) {
	valid := []struct{ url, branch string }{
		{"https://github.com/org/repo.git", "main"},
		{"ssh://git@host/org/repo.git", "release/1.2"},
		{"git@github.com:org/repo.git", "feature_x.y"},
		{"https://github.com/org/repo.git", ""}, // empty branch = default
	}
	for _, tc := range valid {
		if err := validateGitInputs(tc.url, tc.branch); err != nil {
			t.Errorf("validateGitInputs(%q, %q) unexpected error: %v", tc.url, tc.branch, err)
		}
	}

	invalid := []struct{ url, branch, reason string }{
		{"--upload-pack=/bin/sh", "main", "option-injection URL"},
		{"ext::sh -c id", "main", "ext transport"},
		{"file:///etc", "main", "non-allowlisted scheme"},
		{"https://github.com/org/repo.git", "-Bevil", "option-injection branch"},
		{"https://github.com/org/repo.git", "br anch", "whitespace in branch"},
		{"https://github.com/org/repo.git", "br;rm -rf", "shell metachars in branch"},
	}
	for _, tc := range invalid {
		if err := validateGitInputs(tc.url, tc.branch); err == nil {
			t.Errorf("validateGitInputs(%q, %q) must fail: %s", tc.url, tc.branch, tc.reason)
		}
	}
}

func TestClampInt32(t *testing.T) {
	cases := []struct {
		in   int
		want int32
	}{
		{0, 0},
		{42, 42},
		{-42, -42},
		{math.MaxInt32, math.MaxInt32},
		{math.MinInt32, math.MinInt32},
		{math.MaxInt32 + 1, math.MaxInt32},
		{math.MinInt32 - 1, math.MinInt32},
	}
	for _, tc := range cases {
		if got := clampInt32(tc.in); got != tc.want {
			t.Errorf("clampInt32(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
