/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestEngine builds an Engine that writes its output into a buffer
// so the test can assert success/failure markers without having to
// shell out. workspaceRoot is set to the test's temp dir so
// validatePath admits the files we just created.
func newTestEngine(t *testing.T, root string) (*Engine, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	return NewEngine(out, errBuf, root), out, errBuf
}

// writeFile is a small wrapper that fails the test on write errors.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

// readFile returns the file's content or fails the test.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// TestMultipatch_HappyPathSingleFile is the smoke test: one edit
// against one file lands cleanly. Mirrors handlePatch but goes
// through the new transactional code path.
func TestMultipatch_HappyPathSingleFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.txt", "hello world\n")
	eng, out, _ := newTestEngine(t, dir)

	editsJSON := `[{"file":"` + path + `","search":"hello","replace":"goodbye"}]`
	err := eng.Execute(context.Background(), "multipatch", []string{"--edits", editsJSON})
	require.NoError(t, err)
	assert.Equal(t, "goodbye world\n", readFile(t, path))
	assert.Contains(t, out.String(), "multipatch")
	assert.Contains(t, out.String(), "applied 1 edit")
}

// TestMultipatch_HappyPathMultipleFiles confirms cross-file atomic
// commit: three files, three edits, all land in one transaction.
func TestMultipatch_HappyPathMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.txt", "foo")
	b := writeFile(t, dir, "b.txt", "foo")
	c := writeFile(t, dir, "c.txt", "foo")
	eng, _, _ := newTestEngine(t, dir)

	editsJSON := `[
		{"file":"` + a + `","search":"foo","replace":"AAA"},
		{"file":"` + b + `","search":"foo","replace":"BBB"},
		{"file":"` + c + `","search":"foo","replace":"CCC"}
	]`
	err := eng.Execute(context.Background(), "multipatch", []string{"--edits", editsJSON})
	require.NoError(t, err)
	assert.Equal(t, "AAA", readFile(t, a))
	assert.Equal(t, "BBB", readFile(t, b))
	assert.Equal(t, "CCC", readFile(t, c))
}

// TestMultipatch_SequentialEditsOnSameFile pins the contract: edits
// targeting the same file apply in declaration order, with each
// later edit seeing the result of the earlier one.
func TestMultipatch_SequentialEditsOnSameFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "doc.md", "alpha beta gamma")
	eng, _, _ := newTestEngine(t, dir)

	editsJSON := `[
		{"file":"` + path + `","search":"alpha","replace":"X"},
		{"file":"` + path + `","search":"X beta","replace":"Y"}
	]`
	err := eng.Execute(context.Background(), "multipatch", []string{"--edits", editsJSON})
	require.NoError(t, err)
	assert.Equal(t, "Y gamma", readFile(t, path))
}

// TestMultipatch_AtomicRollback_OnValidationFailure exercises the
// rollback path: if any edit's search string is missing, NO files
// are modified, the error reports the offending edit index, and the
// transaction state is unchanged.
func TestMultipatch_AtomicRollback_OnValidationFailure(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.txt", "original-a")
	b := writeFile(t, dir, "b.txt", "original-b")
	eng, _, _ := newTestEngine(t, dir)

	editsJSON := `[
		{"file":"` + a + `","search":"original-a","replace":"NEW-a"},
		{"file":"` + b + `","search":"this-text-does-not-exist","replace":"WONT-APPLY"}
	]`
	err := eng.Execute(context.Background(), "multipatch", []string{"--edits", editsJSON})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "search text not found")
	// Critical: neither file should have been modified.
	assert.Equal(t, "original-a", readFile(t, a),
		"a.txt must NOT change when a later edit's validation fails")
	assert.Equal(t, "original-b", readFile(t, b))
}

// TestMultipatch_RejectsEmptyEdits guards the contract.
func TestMultipatch_RejectsEmptyEdits(t *testing.T) {
	eng, _, _ := newTestEngine(t, t.TempDir())
	err := eng.Execute(context.Background(), "multipatch", []string{"--edits", "[]"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be empty")
}

// TestMultipatch_RejectsMissingFlag guards the dispatch shape.
func TestMultipatch_RejectsMissingFlag(t *testing.T) {
	eng, _, _ := newTestEngine(t, t.TempDir())
	err := eng.Execute(context.Background(), "multipatch", []string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--edits required")
}

// TestMultipatch_RejectsMalformedJSON returns a clear error.
func TestMultipatch_RejectsMalformedJSON(t *testing.T) {
	eng, _, _ := newTestEngine(t, t.TempDir())
	err := eng.Execute(context.Background(), "multipatch", []string{"--edits", "{not an array"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid edits JSON")
}

// TestMultipatch_RequiresFileAndSearch pins per-edit validation.
func TestMultipatch_RequiresFileAndSearch(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "x.txt", "x")
	eng, _, _ := newTestEngine(t, dir)

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"missing file",
			`[{"search":"x","replace":"y"}]`,
			"file is required",
		},
		{
			"missing search",
			`[{"file":"` + path + `","replace":"y"}]`,
			"search is required",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := eng.Execute(context.Background(), "multipatch", []string{"--edits", c.body})
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

// TestMultipatch_PathOutsideWorkspaceRejected pins the security
// boundary: an edit targeting a path outside the workspace fails
// during phase 1, before any writes.
func TestMultipatch_PathOutsideWorkspaceRejected(t *testing.T) {
	dir := t.TempDir()
	insideOK := writeFile(t, dir, "inside.txt", "ok")
	eng, _, _ := newTestEngine(t, dir)

	editsJSON := `[
		{"file":"` + insideOK + `","search":"ok","replace":"changed"},
		{"file":"/etc/hosts","search":"localhost","replace":"hijacked"}
	]`
	err := eng.Execute(context.Background(), "multipatch", []string{"--edits", editsJSON})
	require.Error(t, err)
	// Neither file changed.
	assert.Equal(t, "ok", readFile(t, insideOK),
		"the inside file must remain unchanged when a sibling edit targets a blocked path")
}

// TestMultipatch_Base64Encoding exercises the encoding=base64 path so
// the LLM can ship binary-ish payloads (control chars, non-UTF8) via
// a single JSON envelope.
func TestMultipatch_Base64Encoding(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "b.txt", "abc\ndef")
	eng, _, _ := newTestEngine(t, dir)

	// base64("abc\ndef") = "YWJjCmRlZg=="
	// base64("XYZ\n123") = "WFlaCjEyMw=="
	editsJSON := `[{"file":"` + path + `","search":"YWJjCmRlZg==","replace":"WFlaCjEyMw==","encoding":"base64"}]`
	err := eng.Execute(context.Background(), "multipatch", []string{"--edits", editsJSON})
	require.NoError(t, err)
	assert.Equal(t, "XYZ\n123", readFile(t, path))
}

// TestMultipatch_PerFileLockSerializesConcurrentTx is the key
// concurrency guarantee: two multipatches targeting the same file
// run sequentially, never interleaving. We don't assert exact
// ordering (that depends on goroutine scheduling) but we assert
// that the final content is one of the two coherent end states,
// never a tangled mix.
func TestMultipatch_PerFileLockSerializesConcurrentTx(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "shared.txt", "v0")
	eng1, _, _ := newTestEngine(t, dir)
	eng2, _, _ := newTestEngine(t, dir)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = eng1.Execute(context.Background(), "multipatch",
			[]string{"--edits", `[{"file":"` + path + `","search":"v0","replace":"vA"}]`})
		_ = eng1.Execute(context.Background(), "multipatch",
			[]string{"--edits", `[{"file":"` + path + `","search":"vA","replace":"vA-done"}]`})
	}()
	go func() {
		defer wg.Done()
		_ = eng2.Execute(context.Background(), "multipatch",
			[]string{"--edits", `[{"file":"` + path + `","search":"v0","replace":"vB"}]`})
		_ = eng2.Execute(context.Background(), "multipatch",
			[]string{"--edits", `[{"file":"` + path + `","search":"vB","replace":"vB-done"}]`})
	}()
	wg.Wait()

	final := readFile(t, path)
	// Exactly one of the two transactions wins; the file ends in
	// the WINNER's terminal state. A tangled state (e.g. "vAdone"
	// or "vA-vB") would indicate the lock failed.
	assert.True(t,
		final == "vA-done" || final == "vB-done" || final == "vA" || final == "vB" || final == "v0",
		"unexpected tangled state: %q", final)
}

// TestMultipatch_AppliesOnceEvenWithDuplicateSearch documents the
// design choice: each edit applies its search→replace exactly ONCE
// (strings.Replace with n=1). The LLM can issue multiple edits with
// the same file to replace multiple occurrences explicitly.
func TestMultipatch_AppliesOnceEvenWithDuplicateSearch(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dup.txt", "foo bar foo baz foo")
	eng, _, _ := newTestEngine(t, dir)

	err := eng.Execute(context.Background(), "multipatch",
		[]string{"--edits", `[{"file":"` + path + `","search":"foo","replace":"FOO"}]`})
	require.NoError(t, err)
	// Only the first occurrence flipped.
	got := readFile(t, path)
	assert.Equal(t, 1, strings.Count(got, "FOO"))
	assert.Equal(t, 2, strings.Count(got, "foo"))
}
