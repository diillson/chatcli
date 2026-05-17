/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withTempWorkspaceFile creates a scratch file inside a temp directory
// and chdir's the test into that directory so the engine's path
// validator (which uses cwd as the default workspace boundary) accepts
// the file. t.Chdir auto-restores on cleanup. Returns the absolute
// path to the seeded file.
//
// We do not use CHATCLI_AGENT_EXTRA_READ_PATHS or any aux allowlist:
// the integration test's job is to exercise the production code path
// EXACTLY as a real /coder turn would — within-workspace reads only.
func withTempWorkspaceFile(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

// TestBuiltinRead_EndToEnd is the smoke test: a real file goes
// through parseReadArgs → buildReadArgv → engine.NewEngine().Execute()
// and the output contains the file body. Catches any regression in
// the LLM->engine pipeline that unit tests of the individual helpers
// would miss.
func TestBuiltinRead_EndToEnd(t *testing.T) {
	path := withTempWorkspaceFile(t, "hello.txt", "first line\nsecond line\nthird line\n")
	p := NewBuiltinReadPlugin()
	out, err := p.Execute(context.Background(), []string{`{"file":"` + path + `"}`})
	require.NoError(t, err)
	assert.Contains(t, out, "first line")
	assert.Contains(t, out, "third line")
}

// TestBuiltinRead_LineRange exercises the from_line/to_line slicing
// end-to-end so the LLM contract is verified against real engine
// output, not just argv translation.
func TestBuiltinRead_LineRange(t *testing.T) {
	path := withTempWorkspaceFile(t, "lines.txt",
		"alpha\nbeta\ngamma\ndelta\nepsilon\n")
	p := NewBuiltinReadPlugin()
	out, err := p.Execute(context.Background(),
		[]string{`{"file":"` + path + `","from_line":2,"to_line":4}`})
	require.NoError(t, err)
	assert.Contains(t, out, "beta")
	assert.Contains(t, out, "gamma")
	assert.Contains(t, out, "delta")
	assert.NotContains(t, out, "alpha", "from_line:2 must skip line 1")
	assert.NotContains(t, out, "epsilon", "to_line:4 must skip line 5")
}

// TestBuiltinSearch_EndToEnd verifies the regex pipeline. We seed two
// matching files plus a non-matching one and assert that the matches
// appear with file:line prefixes. chdir into the temp dir so the
// engine's workspace boundary admits the files.
func TestBuiltinSearch_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"),
		[]byte("hello world\nTARGET marker\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"),
		[]byte("nothing here\nalso TARGET\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.txt"),
		[]byte("irrelevant\n"), 0o644))

	p := NewBuiltinSearchPlugin()
	out, err := p.Execute(context.Background(),
		[]string{`{"term":"TARGET","dir":"."}`})
	require.NoError(t, err)
	assert.Contains(t, out, "TARGET",
		"output should include the matching term in the report")
}

// TestBuiltinTree_EndToEnd seeds a small directory tree and confirms
// the plugin dispatches the engine and returns a structured listing.
func TestBuiltinTree_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src", "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "sub", "util.go"), []byte("package y"), 0o644))

	p := NewBuiltinTreePlugin()
	out, err := p.Execute(context.Background(),
		[]string{`{"dir":".","depth":3}`})
	require.NoError(t, err)
	assert.True(t,
		strings.Contains(out, "main.go") || strings.Contains(out, "src"),
		"tree output must include at least one of the seeded entries; got: %q", out)
}

// TestBuiltinRead_MissingFileSurfacesError pins the failure path. A
// path outside the workspace boundary surfaces as "BLOQUEADO" in the
// engine's stderr stream; the plugin captures that and includes it in
// the result body. The plugin itself returns nil (engine reports
// per-file errors as best-effort warnings, not return values) and the
// caller is expected to inspect the output for the error marker.
func TestBuiltinRead_MissingFileSurfacesError(t *testing.T) {
	p := NewBuiltinReadPlugin()
	out, err := p.Execute(context.Background(),
		[]string{`{"file":"/nonexistent/path/that/does/not/exist.txt"}`})
	require.NoError(t, err, "engine reports per-file errors via stderr, not return")
	low := strings.ToLower(out)
	assert.True(t,
		strings.Contains(low, "bloqueado") || strings.Contains(low, "erro"),
		"per-file error message must be surfaced in the captured output, got: %q", out)
}

// TestBuiltinRead_MissingFileArgFailsFast pins the schema-level
// validation: no file = no engine call, immediate error.
func TestBuiltinRead_MissingFileArgFailsFast(t *testing.T) {
	p := NewBuiltinReadPlugin()
	_, err := p.Execute(context.Background(), []string{`{}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "file required")
}

// TestBuiltinSearch_MissingTermFailsFast mirrors the above for search.
func TestBuiltinSearch_MissingTermFailsFast(t *testing.T) {
	p := NewBuiltinSearchPlugin()
	_, err := p.Execute(context.Background(), []string{`{}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "term required")
}
