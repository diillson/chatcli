/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestExtractStringArg_AllShapes is the regression matrix for the args
// extractor used by every plugin's DescribeCall. The original coverage
// of extractStringArg sat at 25 percent because only the JSON-envelope
// path was exercised by the websearch tests. This matrix pins all
// branches: empty, flag-style, positional-fallback, multi-key fallback.
func TestExtractStringArg_AllShapes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		keys []string
		want string
	}{
		{
			name: "empty args returns empty",
			args: nil,
			keys: []string{"query"},
			want: "",
		},
		{
			name: "flat JSON top-level match",
			args: []string{`{"query":"golang errgroup"}`},
			keys: []string{"query"},
			want: "golang errgroup",
		},
		{
			name: "nested @coder envelope match",
			args: []string{`{"cmd":"search","args":{"query":"login"}}`},
			keys: []string{"query"},
			want: "login",
		},
		{
			name: "first key in keys list wins",
			args: []string{`{"q":"second","query":"first"}`},
			keys: []string{"query", "q"},
			want: "first",
		},
		{
			name: "second key is fallback when first missing",
			args: []string{`{"q":"fallback"}`},
			keys: []string{"query", "q"},
			want: "fallback",
		},
		{
			name: "flag style --key value",
			args: []string{"search", "--query", "kubernetes basics"},
			keys: []string{"query"},
			want: "kubernetes basics",
		},
		{
			name: "flag style --key=value",
			args: []string{"--query=golang"},
			keys: []string{"query"},
			want: "golang",
		},
		{
			name: "double-quoted flag value",
			args: []string{"--query", `"quoted value"`},
			keys: []string{"query"},
			want: "quoted value",
		},
		{
			name: "single-quoted flag value",
			args: []string{"--query", "'single quoted'"},
			keys: []string{"query"},
			want: "single quoted",
		},
		{
			name: "positional fallback after JSON failure",
			args: []string{"plain text without flags"},
			keys: []string{"query"},
			want: "plain text without flags",
		},
		{
			// The positional fallback skips the flag token but does NOT
			// track its value pairing — the next non-flag string wins.
			// We pin this behavior so future refactors that reshape the
			// fallback (e.g. to skip flag values too) trip a test.
			name: "positional fallback takes the value adjacent to a flag",
			args: []string{"--unknown", "value", "real arg"},
			keys: []string{"query"},
			want: "value",
		},
		{
			name: "skips JSON-prefixed positional args",
			args: []string{`{"unrelated":"x"}`, "real arg"},
			keys: []string{"query"},
			want: "real arg",
		},
		{
			name: "non-string JSON value is ignored",
			args: []string{`{"query":42}`},
			keys: []string{"query"},
			want: "",
		},
		{
			name: "missing key in nested args falls back to positional",
			args: []string{`{"args":{}}`, "fallback positional"},
			keys: []string{"query"},
			want: "fallback positional",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, extractStringArg(c.args, c.keys...))
		})
	}
}

// TestExtractNestedArg_OnlyLooksInsideArgs guarantees the helper does
// NOT collide with the outer "cmd" field when extracting an inner value
// of the same name. This is the bug the @coder exec / @park for_cmd
// describers worked around.
func TestExtractNestedArg_OnlyLooksInsideArgs(t *testing.T) {
	args := []string{`{"cmd":"exec","args":{"cmd":"go test ./..."}}`}
	assert.Equal(t, "go test ./...", extractNestedArg(args, "cmd", "command"))
}

// TestExtractNestedArg_FallsBackToFlagsWhenNoJSON ensures the helper
// handles positional argv form gracefully.
func TestExtractNestedArg_FallsBackToFlagsWhenNoJSON(t *testing.T) {
	args := []string{"exec", "--cmd", "ls -la"}
	assert.Equal(t, "ls -la", extractNestedArg(args, "cmd"))
}

// TestExtractNestedArg_EmptyArgs returns empty without panic.
func TestExtractNestedArg_EmptyArgs(t *testing.T) {
	assert.Empty(t, extractNestedArg(nil, "cmd"))
	assert.Empty(t, extractNestedArg([]string{}, "cmd"))
}

// TestExtractNestedArg_MalformedJSONFallsBack ensures broken JSON
// doesn't crash; helper falls through to the flag-style scan.
func TestExtractNestedArg_MalformedJSONFallsBack(t *testing.T) {
	args := []string{"{broken json"}
	// No flag match → empty.
	assert.Empty(t, extractNestedArg(args, "cmd"))
}

// TestTrimQuotes_BoundaryCases pins the helper that strips a single
// matching quote pair. Mismatched or single-char strings stay as-is.
func TestTrimQuotes_BoundaryCases(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{`"`, `"`}, // single char
		{`"x"`, "x"},
		{"'x'", "x"},
		{`"unclosed`, `"unclosed`},   // no closing quote
		{`unclosed"`, `unclosed"`},   // no opening quote
		{`"x'`, `"x'`},               // mismatched
		{`""`, ""},                    // empty quoted
		{`'something'`, "something"},
		{`"with spaces"`, "with spaces"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			assert.Equal(t, c.want, trimQuotes(c.in))
		})
	}
}

// TestExtractQueryArg_TriesAliases pins that the alias list
// (query/q/term/search) is honored.
func TestExtractQueryArg_TriesAliases(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{`{"query":"x"}`}, "x"},
		{[]string{`{"q":"x"}`}, "x"},
		{[]string{`{"term":"x"}`}, "x"},
		{[]string{`{"search":"x"}`}, "x"},
		{[]string{`{"unrelated":"x"}`}, ""},
	}
	for _, c := range cases {
		t.Run(c.args[0], func(t *testing.T) {
			assert.Equal(t, c.want, extractQueryArg(c.args))
		})
	}
}

// TestExtractURLArg_TriesAliases pins the same for url/uri/address.
func TestExtractURLArg_TriesAliases(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{`{"url":"https://x"}`}, "https://x"},
		{[]string{`{"uri":"https://x"}`}, "https://x"},
		{[]string{`{"address":"https://x"}`}, "https://x"},
	}
	for _, c := range cases {
		t.Run(c.args[0], func(t *testing.T) {
			assert.Equal(t, c.want, extractURLArg(c.args))
		})
	}
}

// TestExtractPathArg_TriesAliases mirrors for file/path/filepath.
func TestExtractPathArg_TriesAliases(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{`{"file":"/x"}`}, "/x"},
		{[]string{`{"path":"/x"}`}, "/x"},
		{[]string{`{"filepath":"/x"}`}, "/x"},
	}
	for _, c := range cases {
		t.Run(c.args[0], func(t *testing.T) {
			assert.Equal(t, c.want, extractPathArg(c.args))
		})
	}
}
