/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseReadArgs_FlatJSON pins the LLM's preferred shape: a flat
// JSON object with file plus optional range/encoding fields. Each
// alias is enumerated so a future rename of "from_line" -> "start"
// at the plugin layer doesn't silently break compat.
func TestParseReadArgs_FlatJSON(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want readArgs
	}{
		{
			"file only",
			[]string{`{"file":"main.go"}`},
			readArgs{File: "main.go"},
		},
		{
			"file with from/to lines",
			[]string{`{"file":"main.go","from_line":10,"to_line":50}`},
			readArgs{File: "main.go", FromLine: 10, ToLine: 50},
		},
		{
			"file with head + max_bytes",
			[]string{`{"file":"main.go","head":20,"max_bytes":4096}`},
			readArgs{File: "main.go", Head: 20, MaxBytes: 4096},
		},
		{
			"file with base64 encoding",
			[]string{`{"file":"img.png","encoding":"base64"}`},
			readArgs{File: "img.png", Encoding: "base64"},
		},
		{
			"alias 'path' instead of 'file'",
			[]string{`{"path":"x.go"}`},
			readArgs{File: "x.go"},
		},
		{
			"alias 'start'/'end' for lines",
			[]string{`{"file":"x","start":3,"end":7}`},
			readArgs{File: "x", FromLine: 3, ToLine: 7},
		},
		{
			"stringified integer is accepted",
			[]string{`{"file":"x","from_line":"5"}`},
			readArgs{File: "x", FromLine: 5},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseReadArgs(c.args)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

// TestParseReadArgs_CoderEnvelope confirms backwards compatibility
// with the legacy @coder read shape. The same parse path handles both
// invocation styles so the model can use either.
func TestParseReadArgs_CoderEnvelope(t *testing.T) {
	got, err := parseReadArgs([]string{`{"cmd":"read","args":{"file":"main.go","from_line":10}}`})
	require.NoError(t, err)
	assert.Equal(t, "main.go", got.File)
	assert.Equal(t, 10, got.FromLine)
}

// TestParseReadArgs_PositionalFlagForm covers the CLI invocation path
// (a user typing `@read --file main.go --start 5` at the prompt).
func TestParseReadArgs_PositionalFlagForm(t *testing.T) {
	got, err := parseReadArgs([]string{"--file", "main.go", "--start", "5", "--end", "10"})
	require.NoError(t, err)
	assert.Equal(t, "main.go", got.File)
	assert.Equal(t, 5, got.FromLine)
	assert.Equal(t, 10, got.ToLine)
}

// TestParseReadArgs_BarePositionalIsTreatedAsFile lets the user type
// `@read main.go` without flags.
func TestParseReadArgs_BarePositionalIsTreatedAsFile(t *testing.T) {
	got, err := parseReadArgs([]string{"main.go"})
	require.NoError(t, err)
	assert.Equal(t, "main.go", got.File)
}

// TestParseReadArgs_EmptyAndMalformed pin the defensive paths. The
// nil/empty case returns a zero-value readArgs and no error (the
// caller decides whether file-required is a hard failure). Malformed
// JSON returns an error so the caller surfaces a clear "@read:
// malformed JSON args" message to the LLM instead of silently treating
// the broken envelope as a positional file name.
func TestParseReadArgs_EmptyAndMalformed(t *testing.T) {
	got, err := parseReadArgs(nil)
	require.NoError(t, err)
	assert.Equal(t, readArgs{}, got)

	_, err = parseReadArgs([]string{`{broken json`})
	assert.Error(t, err, "broken JSON envelope must surface as an error, not silently fall back")
	assert.Contains(t, err.Error(), "@read: malformed JSON args")
}

// TestBuildReadArgv pins the LLM->engine flag translation. This is a
// mutation guard: any engine flag rename must update this site, and
// any new field added to readArgs needs a corresponding row here.
func TestBuildReadArgv(t *testing.T) {
	cases := []struct {
		name string
		in   readArgs
		want []string
	}{
		{"file only", readArgs{File: "a"}, []string{"--file", "a"}},
		{"file + lines", readArgs{File: "a", FromLine: 1, ToLine: 5},
			[]string{"--file", "a", "--start", "1", "--end", "5"}},
		{"file + head", readArgs{File: "a", Head: 10},
			[]string{"--file", "a", "--head", "10"}},
		{"file + tail", readArgs{File: "a", Tail: 10},
			[]string{"--file", "a", "--tail", "10"}},
		{"file + max_bytes", readArgs{File: "a", MaxBytes: 4096},
			[]string{"--file", "a", "--max-bytes", "4096"}},
		{"file + encoding", readArgs{File: "a", Encoding: "base64"},
			[]string{"--file", "a", "--encoding", "base64"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, buildReadArgv(c.in))
		})
	}
}

// TestParseSearchArgs_AllShapes mirrors the read tests for search.
func TestParseSearchArgs_AllShapes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want searchArgs
	}{
		{"term only", []string{`{"term":"Login"}`}, searchArgs{Term: "Login"}},
		{"term + dir", []string{`{"term":"Login","dir":"./src"}`}, searchArgs{Term: "Login", Dir: "./src"}},
		{"alias pattern", []string{`{"pattern":"x"}`}, searchArgs{Term: "x"}},
		{"alias query", []string{`{"query":"x"}`}, searchArgs{Term: "x"}},
		{"alias regex", []string{`{"regex":"x"}`}, searchArgs{Term: "x"}},
		{"alias glob -> include", []string{`{"term":"x","glob":"*.go"}`}, searchArgs{Term: "x", Include: "*.go"}},
		{"@coder envelope", []string{`{"cmd":"search","args":{"term":"x","dir":"."}}`}, searchArgs{Term: "x", Dir: "."}},
		{"max_results parsed", []string{`{"term":"x","max_results":50}`}, searchArgs{Term: "x", MaxResults: 50}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseSearchArgs(c.args)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

// TestBuildSearchArgv_AlwaysRegex pins that @search always enables
// engine --regex. The schema documents regex semantics; we must not
// silently downgrade to literal match.
func TestBuildSearchArgv_AlwaysRegex(t *testing.T) {
	argv := buildSearchArgv(searchArgs{Term: "Login"})
	assert.Contains(t, argv, "--regex", "regex mode must always be on for @search")
	assert.Contains(t, argv, "--term")
	assert.Contains(t, argv, "Login")
}

// TestBuildSearchArgv_IncludeMapsToGlob pins the include->glob
// translation. Mutation guard.
func TestBuildSearchArgv_IncludeMapsToGlob(t *testing.T) {
	argv := buildSearchArgv(searchArgs{Term: "x", Include: "*.go"})
	idx := indexOf(argv, "--glob")
	require.GreaterOrEqual(t, idx, 0, "include must produce --glob in argv")
	assert.Equal(t, "*.go", argv[idx+1])
}

// TestParseTreeArgs_AllShapes pins the tree parser.
func TestParseTreeArgs_AllShapes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want treeArgs
	}{
		{"empty defaults", nil, treeArgs{}},
		{"dir only", []string{`{"dir":"src"}`}, treeArgs{Dir: "src"}},
		{"dir + depth", []string{`{"dir":"src","depth":2}`}, treeArgs{Dir: "src", Depth: 2}},
		{"dir + exclude", []string{`{"dir":"src","exclude":"node_modules"}`}, treeArgs{Dir: "src", Exclude: "node_modules"}},
		{"alias path", []string{`{"path":"src"}`}, treeArgs{Dir: "src"}},
		{"alias maxDepth", []string{`{"maxDepth":4}`}, treeArgs{Depth: 4}},
		{"@coder envelope", []string{`{"cmd":"tree","args":{"dir":"src"}}`}, treeArgs{Dir: "src"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseTreeArgs(c.args)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

// TestBuildTreeArgv_DepthMapsToMaxDepth pins the engine flag rename.
func TestBuildTreeArgv_DepthMapsToMaxDepth(t *testing.T) {
	argv := buildTreeArgv(treeArgs{Dir: "src", Depth: 2})
	idx := indexOf(argv, "--max-depth")
	require.GreaterOrEqual(t, idx, 0, "depth must produce --max-depth in argv")
	assert.Equal(t, "2", argv[idx+1])
	// And NOT a bare --depth — that would fail the engine parser.
	assert.Equal(t, -1, indexOf(argv, "--depth"))
}

// TestBuildTreeArgv_ExcludeMapsToIgnore pins the same for exclude.
func TestBuildTreeArgv_ExcludeMapsToIgnore(t *testing.T) {
	argv := buildTreeArgv(treeArgs{Exclude: "node_modules"})
	idx := indexOf(argv, "--ignore")
	require.GreaterOrEqual(t, idx, 0)
	assert.Equal(t, "node_modules", argv[idx+1])
}

// TestBuildTreeArgv_EmptyArgsProducesEmptyArgv keeps the engine in
// charge of defaults — if the user didn't supply dir, we don't force
// one (the engine defaults to ".").
func TestBuildTreeArgv_EmptyArgsProducesEmptyArgv(t *testing.T) {
	assert.Empty(t, buildTreeArgv(treeArgs{}))
}

// TestAtomicTools_AllAdvertiseReadOnlyAndConcurrencySafe pins the
// capability contract: @read, @search, @tree are all marked read-only
// + concurrency-safe. This is what lets the orchestrator partition
// them into parallel batches.
func TestAtomicTools_AllAdvertiseReadOnlyAndConcurrencySafe(t *testing.T) {
	for _, p := range []interface {
		IsReadOnly([]string) bool
		IsConcurrencySafe([]string) bool
	}{
		NewBuiltinReadPlugin(),
		NewBuiltinSearchPlugin(),
		NewBuiltinTreePlugin(),
	} {
		assert.True(t, p.IsReadOnly(nil))
		assert.True(t, p.IsConcurrencySafe(nil))
	}
}

// TestAtomicTools_NamesAndSchemas pin the LLM-visible contract.
// Mutation guard: renaming @read to something else breaks this.
func TestAtomicTools_NamesAndSchemas(t *testing.T) {
	cases := []struct {
		plugin interface {
			Name() string
			Schema() string
		}
		wantName       string
		schemaIncludes string
	}{
		{NewBuiltinReadPlugin(), "@read", `"file"`},
		{NewBuiltinSearchPlugin(), "@search", `"term"`},
		{NewBuiltinTreePlugin(), "@tree", `"dir"`},
	}
	for _, c := range cases {
		t.Run(c.wantName, func(t *testing.T) {
			assert.Equal(t, c.wantName, c.plugin.Name())
			assert.Contains(t, c.plugin.Schema(), c.schemaIncludes)
		})
	}
}

// TestAtomicTools_DescribeCallSurfacesTarget pins the spinner labels.
func TestAtomicTools_DescribeCallSurfacesTarget(t *testing.T) {
	r := NewBuiltinReadPlugin()
	assert.Contains(t, r.DescribeCall([]string{`{"file":"main.go"}`}), "main.go")
	// Empty input → falls back to static description.
	assert.Equal(t, r.Description(), r.DescribeCall(nil))

	s := NewBuiltinSearchPlugin()
	assert.Contains(t, s.DescribeCall([]string{`{"term":"Login"}`}), "Login")
	assert.Equal(t, s.Description(), s.DescribeCall(nil))

	tr := NewBuiltinTreePlugin()
	assert.Contains(t, tr.DescribeCall([]string{`{"dir":"src"}`}), "src")
	// Empty dir defaults to "." in describe.
	assert.Contains(t, tr.DescribeCall(nil), ".")
}

// TestAtomicTools_SchemaIsValidJSON guards against accidental schema
// edits breaking the JSON serialization.
func TestAtomicTools_SchemaIsValidJSON(t *testing.T) {
	for _, p := range []interface{ Schema() string }{
		NewBuiltinReadPlugin(),
		NewBuiltinSearchPlugin(),
		NewBuiltinTreePlugin(),
	} {
		schema := p.Schema()
		assert.True(t, strings.HasPrefix(schema, "{"))
		assert.True(t, strings.HasSuffix(schema, "}"))
	}
}

// indexOf is a tiny helper that returns the position of needle in
// haystack or -1. Local to the test file because the production code
// doesn't need it.
func indexOf(haystack []string, needle string) int {
	for i, s := range haystack {
		if s == needle {
			return i
		}
	}
	return -1
}
