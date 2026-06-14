package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newWsEngine returns an engine whose workspace root is a fresh temp dir,
// so validatePath permits writes/reads inside it.
func newWsEngine(t *testing.T) (*Engine, *bytes.Buffer, string) {
	t.Helper()
	// Resolve symlinks (macOS /var -> /private/var) so the workspace-boundary
	// check, which canonicalizes paths, sees a consistent root.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	e := NewEngine(&out, &out, root)
	return e, &out, root
}

func TestHandleWrite_Basic(t *testing.T) {
	e, out, root := newWsEngine(t)
	target := filepath.Join(root, "sub", "f.txt")

	if err := e.Execute(context.Background(), "write",
		[]string{"--file", target, "--content", "hello"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "hello" {
		t.Fatalf("content=%q err=%v", got, err)
	}
	if !strings.Contains(out.String(), "escrito") {
		t.Errorf("missing success message: %q", out.String())
	}
}

func TestHandleWrite_Append(t *testing.T) {
	e, _, root := newWsEngine(t)
	target := filepath.Join(root, "f.txt")
	if err := os.WriteFile(target, []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(context.Background(), "write",
		[]string{"--file", target, "--content", "b", "--append"}); err != nil {
		t.Fatalf("write append: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "ab" {
		t.Errorf("append content=%q want ab", got)
	}
}

func TestHandleWrite_Validation(t *testing.T) {
	e, _, _ := newWsEngine(t)
	if err := e.Execute(context.Background(), "write", []string{"--content", "x"}); err == nil {
		t.Error("expected error for missing --file")
	}
	if err := e.Execute(context.Background(), "write", []string{"--file", "f.txt"}); err == nil {
		t.Error("expected error for empty --content")
	}
}

func TestHandleWrite_DecodeError(t *testing.T) {
	e, _, root := newWsEngine(t)
	target := filepath.Join(root, "f.bin")
	// Invalid base64 triggers the decode error path.
	err := e.Execute(context.Background(), "write",
		[]string{"--file", target, "--content", "!!!notbase64!!!", "--encoding", "base64"})
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

func TestHandlePatch_Basic(t *testing.T) {
	e, out, root := newWsEngine(t)
	target := filepath.Join(root, "f.txt")
	if err := os.WriteFile(target, []byte("foo bar baz"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(context.Background(), "patch",
		[]string{"--file", target, "--search", "bar", "--replace", "QUX"}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "foo QUX baz" {
		t.Errorf("patched=%q", got)
	}
	if !strings.Contains(out.String(), "Patch aplicado") {
		t.Errorf("missing success: %q", out.String())
	}
	// A backup must have been created.
	if !fileExists(target + ".bak") {
		t.Error("expected backup file")
	}
}

func TestHandlePatch_NotFound(t *testing.T) {
	e, _, root := newWsEngine(t)
	target := filepath.Join(root, "f.txt")
	if err := os.WriteFile(target, []byte("foo"), 0600); err != nil {
		t.Fatal(err)
	}
	err := e.Execute(context.Background(), "patch",
		[]string{"--file", target, "--search", "absent", "--replace", "x"})
	if err == nil {
		t.Error("expected not-found error")
	}
}

func TestHandlePatch_ReadError(t *testing.T) {
	e, _, root := newWsEngine(t)
	missing := filepath.Join(root, "nope.txt")
	err := e.Execute(context.Background(), "patch",
		[]string{"--file", missing, "--search", "x", "--replace", "y"})
	if err == nil || !strings.Contains(err.Error(), "leitura") {
		t.Fatalf("expected read error, got %v", err)
	}
}

func TestHandlePatch_RequiresFlags(t *testing.T) {
	e, _, _ := newWsEngine(t)
	if err := e.Execute(context.Background(), "patch", []string{"--file", "f.txt"}); err == nil {
		t.Error("expected error when --search missing")
	}
}

func TestHandleRollback(t *testing.T) {
	e, _, root := newWsEngine(t)
	target := filepath.Join(root, "f.txt")
	if err := os.WriteFile(target, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".bak", []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(context.Background(), "rollback", []string{"--file", target}); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "old" {
		t.Errorf("rolled back content=%q want old", got)
	}
}

func TestHandleRollback_MissingBackup(t *testing.T) {
	e, _, root := newWsEngine(t)
	target := filepath.Join(root, "f.txt")
	if err := os.WriteFile(target, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	err := e.Execute(context.Background(), "rollback", []string{"--file", target})
	if err == nil || !strings.Contains(err.Error(), "backup") {
		t.Fatalf("expected backup error, got %v", err)
	}
}

func TestHandleSearch_Fallback(t *testing.T) {
	e, out, root := newWsEngine(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\nNEEDLE here\nomega\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Force the pure-Go fallback by clearing PATH so rg can't be found.
	t.Setenv("PATH", "")
	if err := e.Execute(context.Background(), "search",
		[]string{"--term", "NEEDLE", "--dir", root}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out.String(), "NEEDLE") {
		t.Errorf("search output = %q", out.String())
	}
}

func TestHandleSearch_RequiresTerm(t *testing.T) {
	e, _, root := newWsEngine(t)
	if err := e.Execute(context.Background(), "search", []string{"--dir", root}); err == nil {
		t.Error("expected error for missing --term")
	}
}

func TestHandleSearch_InvalidRegex(t *testing.T) {
	e, _, root := newWsEngine(t)
	t.Setenv("PATH", "") // force fallback path where regex is compiled
	err := e.Execute(context.Background(), "search",
		[]string{"--term", "(", "--regex", "--dir", root})
	if err == nil || !strings.Contains(err.Error(), "regex") {
		t.Fatalf("expected regex error, got %v", err)
	}
}

func TestHandleTree(t *testing.T) {
	e, out, root := newWsEngine(t)
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "main.go"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(context.Background(), "tree", []string{"--dir", root}); err != nil {
		t.Fatalf("tree: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "pkg") || !strings.Contains(s, "main.go") {
		t.Errorf("tree output = %q", s)
	}
}

func TestHandleClean_DryRunAndForce(t *testing.T) {
	e, out, root := newWsEngine(t)
	bak := filepath.Join(root, "f.txt.bak")
	if err := os.WriteFile(bak, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	// Dry run lists but does not delete.
	if err := e.Execute(context.Background(), "clean", []string{"--dir", root}); err != nil {
		t.Fatalf("clean dry-run: %v", err)
	}
	if !fileExists(bak) {
		t.Error("dry-run should not delete")
	}
	if !strings.Contains(out.String(), "Dry-run") {
		t.Errorf("missing dry-run msg: %q", out.String())
	}

	// Force deletes.
	if err := e.Execute(context.Background(), "clean", []string{"--dir", root, "--force"}); err != nil {
		t.Fatalf("clean force: %v", err)
	}
	if fileExists(bak) {
		t.Error("force should delete the .bak file")
	}
}
