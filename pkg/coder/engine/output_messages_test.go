/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The engine's results are read back by the model, so they are pinned in
// stable English regardless of the user's locale.

func TestHandleRead_OutputMarkers(t *testing.T) {
	e, out, root := newWsEngine(t)
	ctx := context.Background()
	f := filepath.Join(root, "a.txt")
	if err := os.WriteFile(f, []byte("one\ntwo\nthree"), 0600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"text", []string{"--file", f}, []string{"<<< BEGIN FILE: " + f + " >>>", "<<< END FILE: " + f + " >>>"}},
		{"base64", []string{"--file", f, "--encoding", "base64"}, []string{"<<< BEGIN FILE (base64): " + f + " >>>", "<<< END FILE: " + f + " >>>"}},
		{"invalid range", []string{"--file", f, "--start", "9"}, []string{"❌ Invalid range for '" + f + "'"}},
		{"truncated", []string{"--file", f, "--max-bytes", "3"}, []string{"... [TRUNCATED AT 3 BYTES] ..."}},
		{"missing", []string{"--file", filepath.Join(root, "nope.txt")}, []string{"❌ ERROR READING '"}},
		{"outside workspace", []string{"--file", "/nonexistent/outside/x.txt"}, []string{"❌ BLOCKED /nonexistent/outside/x.txt"}},
	}
	for _, c := range cases {
		out.Reset()
		if err := e.Execute(ctx, "read", c.args); err != nil {
			t.Fatalf("%s: read: %v", c.name, err)
		}
		for _, w := range c.want {
			if !strings.Contains(out.String(), w) {
				t.Errorf("%s: missing %q in %q", c.name, w, out.String())
			}
		}
	}
}

func TestHandleRead_LineCapHint(t *testing.T) {
	e, out, root := newWsEngine(t)
	f := filepath.Join(root, "long.txt")
	if err := os.WriteFile(f, []byte(strings.Repeat("x\n", DefaultMaxLines+10)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(context.Background(), "read", []string{"--file", f}); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out.String(), "lines; continue with --start") {
		t.Errorf("missing paging hint: %q", out.String())
	}
}

func TestHandleRollbackAndClean_Messages(t *testing.T) {
	e, out, root := newWsEngine(t)
	ctx := context.Background()
	f := filepath.Join(root, "f.txt")
	if err := os.WriteFile(f, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := e.Execute(ctx, "clean", []string{"--dir", root}); err != nil {
		t.Fatalf("clean: %v", err)
	}
	if !strings.Contains(out.String(), "No files to clean.") {
		t.Errorf("empty clean: %q", out.String())
	}

	if err := os.WriteFile(f+".bak", []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := e.Execute(ctx, "rollback", []string{"--file", f}); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got, _ := os.ReadFile(f); string(got) != "old" || !strings.Contains(out.String(), "✅ Rollback done.") {
		t.Errorf("rollback content=%q out=%q", got, out.String())
	}

	out.Reset()
	if err := e.Execute(ctx, "clean", []string{"--dir", root}); err != nil {
		t.Fatalf("clean dry-run: %v", err)
	}
	if !strings.Contains(out.String(), "files that would be removed") || !strings.Contains(out.String(), "Use --force to remove them.") {
		t.Errorf("dry-run: %q", out.String())
	}

	out.Reset()
	if err := e.Execute(ctx, "clean", []string{"--dir", root, "--force"}); err != nil {
		t.Fatalf("clean force: %v", err)
	}
	if !strings.Contains(out.String(), "✅ Removed 1 files.") {
		t.Errorf("force clean: %q", out.String())
	}
}

func TestHandleTree_LimitMessage(t *testing.T) {
	e, out, root := newWsEngine(t)
	for _, n := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(root, n), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Execute(context.Background(), "tree", []string{"--dir", root, "--max-entries", "2"}); err != nil {
		t.Fatalf("tree: %v", err)
	}
	if !strings.Contains(out.String(), "... [LIMITED TO 2 ENTRIES] ...") {
		t.Errorf("missing limit marker: %q", out.String())
	}
}

func TestHandleExec_FailureMessage(t *testing.T) {
	e, out, _ := newWsEngine(t)
	if err := e.Execute(context.Background(), "exec", []string{"--cmd", "exit 3"}); err == nil {
		t.Fatal("a failing command must return an error")
	}
	if !strings.Contains(out.String(), "❌ Failed:") {
		t.Errorf("missing failure marker: %q", out.String())
	}
}
