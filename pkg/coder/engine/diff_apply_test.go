package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyUnifiedDiff_HappyPath(t *testing.T) {
	e, out, root := newWsEngine(t)
	target := filepath.Join(root, "f.txt")
	if err := os.WriteFile(target, []byte("line1\nline2\nline3\n"), 0600); err != nil {
		t.Fatal(err)
	}

	diff := "--- a/f.txt\n+++ b/f.txt\n@@ -1,3 +1,3 @@\n line1\n-line2\n+CHANGED\n line3\n"

	if err := e.applyUnifiedDiff(target, diff, "text"); err != nil {
		t.Fatalf("applyUnifiedDiff: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "line1\nCHANGED\nline3\n" {
		t.Errorf("result = %q", got)
	}
	if !strings.Contains(out.String(), "Diff aplicado") {
		t.Errorf("missing success msg: %q", out.String())
	}
}

func TestApplyUnifiedDiff_ViaPatchCommand(t *testing.T) {
	e, _, root := newWsEngine(t)
	target := filepath.Join(root, "g.txt")
	if err := os.WriteFile(target, []byte("a\nb\n"), 0600); err != nil {
		t.Fatal(err)
	}
	diff := "@@ -1,2 +1,2 @@\n a\n-b\n+B\n"
	if err := e.Execute(context.Background(), "patch",
		[]string{"--file", target, "--diff", diff}); err != nil {
		t.Fatalf("patch --diff: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "a\nB\n" {
		t.Errorf("result = %q", got)
	}
}

func TestApplyUnifiedDiff_EmptyDiff(t *testing.T) {
	e, _, root := newWsEngine(t)
	err := e.applyUnifiedDiff(filepath.Join(root, "x"), "no hunks here\n", "text")
	if err == nil {
		t.Fatal("expected error for diff with no hunks")
	}
}

func TestApplyHunksToFile_ReadError(t *testing.T) {
	e, _, root := newWsEngine(t)
	missing := filepath.Join(root, "absent.txt")
	diff := "--- a/absent.txt\n+++ b/absent.txt\n@@ -1 +1 @@\n-x\n+y\n"
	err := e.applyUnifiedDiff(missing, diff, "text")
	if err == nil || !strings.Contains(err.Error(), "leitura") {
		t.Fatalf("expected read error, got %v", err)
	}
}

func TestApplyHunksToFile_Mismatch(t *testing.T) {
	e, _, root := newWsEngine(t)
	target := filepath.Join(root, "h.txt")
	if err := os.WriteFile(target, []byte("actual\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Context line "expected" doesn't match file content "actual".
	diff := "@@ -1 +1 @@\n-expected\n+new\n"
	err := e.applyUnifiedDiff(target, diff, "text")
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected mismatch error, got %v", err)
	}
}

func TestParseUnifiedDiff_BadHunkHeader(t *testing.T) {
	_, err := parseUnifiedDiff("@@ not a valid header @@\n x\n")
	if err == nil {
		t.Error("expected error for bad hunk header")
	}
}

func TestNormalizeDiffPath(t *testing.T) {
	cases := map[string]string{
		"a/foo.go":   "foo.go",
		"b/bar.go":   "bar.go",
		"  baz.go  ": "baz.go",
	}
	for in, want := range cases {
		if got := normalizeDiffPath(in); got != want {
			t.Errorf("normalizeDiffPath(%q)=%q want %q", in, got, want)
		}
	}
}
