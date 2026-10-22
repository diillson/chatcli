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
)

func runDiff(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return NewBuiltinDiffPlugin().Execute(context.Background(), args)
}

func writeDiffFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiffTool_FilesAndText(t *testing.T) {
	t.Chdir(t.TempDir())
	writeDiffFile(t, "a.txt", "one\ntwo\nthree\nfour\nfive\nsix\nseven\n")
	writeDiffFile(t, "b.txt", "one\n2\nthree\nfour\nfive\nsix\nseven\neight\n")
	writeDiffFile(t, "same.txt", "one\ntwo\nthree\nfour\nfive\nsix\nseven\n")
	writeDiffFile(t, "bin1", "abc\x00def")
	writeDiffFile(t, "bin2", "abc\x00xyz")

	out, err := runDiff(t, `{"cmd":"files","args":{"a":"a.txt","b":"b.txt","context":1}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "--- a.txt\n+++ b.txt\n@@ -1,3 +1,3 @@\n one\n-two\n+2\n three\n@@ -7,1 +7,2 @@\n seven\n+eight"
	if out != want {
		t.Fatalf("unified diff mismatch:\n%s\n--- want ---\n%s", out, want)
	}
	for _, c := range []struct{ args, want string }{
		{`{"cmd":"files","args":{"a":"a.txt","b":"same.txt"}}`, "identical: a.txt and same.txt"},
		{`{"cmd":"files","args":{"a":"bin1","b":"bin2"}}`, "binary files differ"},
		{`{"a":"a.txt","b":"b.txt"}`, "@@ -1,7 +1,8 @@"}, // default context 3 merges the two changes
		{`{"cmd":"text","args":{"a":"x\ny","b":"x\nz"}}`, "-y\n\\ No newline at end of file\n+z\n\\ No newline at end of file"},
		{`{"cmd":"text","args":{"a":"","b":"new\n"}}`, "@@ -0,0 +1,1 @@\n+new"},
		{`{"a":"hello","b":"hello"}`, "identical: a and b"},
	} {
		out, err := runDiff(t, c.args)
		if err != nil || !strings.Contains(out, c.want) {
			t.Errorf("%s: err=%v, output missing %q:\n%s", c.args, err, c.want, out)
		}
	}
	if _, err := runDiff(t, `{"cmd":"files","args":{"a":"a.txt","b":"missing.txt"}}`); err == nil {
		t.Fatal("missing file must error")
	}
	if _, err := runDiff(t, `{"cmd":"files","args":{"a":"a.txt","b":"/etc/hosts"}}`); err == nil || !strings.Contains(err.Error(), "outside workspace boundary") {
		t.Fatalf("read outside the workspace must be refused: %v", err)
	}
}

func TestDiffTool_MyersRoundTrip(t *testing.T) {
	// Whatever the script, replaying it over a must rebuild b exactly.
	cases := [][2]string{
		{"a\nb\nc\n", "a\nc\n"}, {"", "a\n"}, {"a\nb\n", ""}, {"x\n", "y\n"},
		{"a\nb\nc\nd\ne\n", "e\nd\nc\nb\na\n"}, {"1\n2\n3\n4\n5\n6\n7\n8\n", "0\n1\n3\n4\n4b\n5\n6\n8\n9\n"},
	}
	for _, c := range cases {
		a, _ := splitDiffLines(c[0])
		b, _ := splitDiffLines(c[1])
		ops, minimal := diffLines(a, b)
		if !minimal {
			t.Fatalf("%q→%q: expected a minimal script", c[0], c[1])
		}
		var got []string
		for _, o := range ops {
			switch o.kind {
			case ' ':
				if a[o.ai] != b[o.bi] {
					t.Fatalf("keep op on unequal lines %q %q", a[o.ai], b[o.bi])
				}
				got = append(got, b[o.bi])
			case '+':
				got = append(got, b[o.bi])
			}
		}
		if strings.Join(got, "\n") != strings.Join(b, "\n") {
			t.Fatalf("%q→%q: replay gave %q", c[0], c[1], got)
		}
	}
	// Past the edit budget the diff degrades to a block replacement, not an error.
	big := make([]string, diffToolMaxEdits+10)
	for i := range big {
		big[i] = "line " + strings.Repeat("x", i%7) + string(rune('a'+i%26))
	}
	if _, minimal := diffLines(big, []string{"other"}); minimal {
		t.Fatal("expected the block-replacement fallback")
	}
	if out := diffTexts("a", "b", strings.Join(big, "\n"), "other\n", 1); !strings.Contains(out, "block replacement") {
		t.Fatalf("fallback must be announced:\n%s", out[max(0, len(out)-200):])
	}
}

func TestDiffTool_Dirs(t *testing.T) {
	t.Chdir(t.TempDir())
	writeDiffFile(t, "v1/keep.go", "same")
	writeDiffFile(t, "v1/old.go", "gone")
	writeDiffFile(t, "v1/changed.go", "before")
	writeDiffFile(t, "v1/.git/HEAD", "ref")
	writeDiffFile(t, "v1/readme.md", "doc")
	writeDiffFile(t, "v2/keep.go", "same")
	writeDiffFile(t, "v2/new.go", "fresh")
	writeDiffFile(t, "v2/changed.go", "after!")
	writeDiffFile(t, "v2/node_modules/x.go", "dep")
	writeDiffFile(t, "v2/readme.md", "doc2")
	out, err := runDiff(t, `{"cmd":"dirs","args":{"a":"v1","b":"v2","glob":"*.go"}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "v1 → v2: 1 added, 1 removed, 1 changed (6 files scanned)\n+ new.go\n- old.go\n~ changed.go"
	if out != want {
		t.Fatalf("dirs mismatch:\n%s\n--- want ---\n%s", out, want)
	}
	if _, err := runDiff(t, `{"cmd":"dirs","args":{"a":"v1","b":"v2","glob":"[bad"}}`); err == nil || !strings.Contains(err.Error(), "bad glob") {
		t.Fatalf("bad glob: %v", err)
	}
}

func TestDiffTool_Apply(t *testing.T) {
	t.Chdir(t.TempDir())
	const original = "package main\n\nfunc main() {\n\told()\n}\n"
	patch := "--- a/main.go\n+++ b/main.go\n@@ -3,3 +3,4 @@\n func main() {\n-\told()\n+\tnew()\n+\tmore()\n }\n"
	writeDiffFile(t, "main.go", original)

	out, err := runDiff(t, `{"cmd":"apply","args":{"file":"main.go","patch":`+quoteJSON(patch)+`}}`)
	if err != nil || !strings.Contains(out, "dry run: patch applies cleanly to main.go (1 hunks, +2 -1 lines, 5 → 6 lines)") {
		t.Fatalf("dry run: %v %q", err, out)
	}
	if data, _ := os.ReadFile("main.go"); string(data) != original {
		t.Fatal("dry run must not write")
	}
	if _, err := os.Stat("main.go.bak"); err == nil {
		t.Fatal("dry run must not create a backup")
	}

	out, err = runDiff(t, `{"cmd":"apply","args":{"file":"main.go","patch":`+quoteJSON(patch)+`,"dry_run":false}}`)
	if err != nil || !strings.Contains(out, "applied to main.go (1 hunks, +2 -1 lines, 5 → 6 lines); backup at main.go.bak") {
		t.Fatalf("apply: %v %q", err, out)
	}
	data, _ := os.ReadFile("main.go")
	if string(data) != "package main\n\nfunc main() {\n\tnew()\n\tmore()\n}\n" {
		t.Fatalf("applied content:\n%q", data)
	}
	if bak, _ := os.ReadFile("main.go.bak"); string(bak) != original {
		t.Fatal("backup must hold the original")
	}

	// Re-applying hits a context mismatch: refused, first failing line named, nothing written.
	_, err = runDiff(t, `{"cmd":"apply","args":{"file":"main.go","patch":`+quoteJSON(patch)+`,"dry_run":false}}`)
	if err == nil || !strings.Contains(err.Error(), `hunk 1 (@@ -3,3 +3,4 @@): line 4 is "\tnew()", patch expects "\told()"`) {
		t.Fatalf("mismatch must be precise: %v", err)
	}
	if after, _ := os.ReadFile("main.go"); string(after) != string(data) {
		t.Fatal("a refused patch must leave the file untouched")
	}
	for _, c := range []struct{ patch, want string }{
		{"no hunks here", "no @@ hunks found"},
		{"@@ -99,1 +99,1 @@\n-x\n+y\n", "past the end"},
		{"@@ -1,1 +1,1 @@\n?bogus\n", "unexpected diff line"},
	} {
		if _, err := runDiff(t, `{"cmd":"apply","args":{"file":"main.go","patch":`+quoteJSON(c.patch)+`}}`); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: want %q, got %v", c.patch, c.want, err)
		}
	}
	// A patch that touches the last line without a newline marker keeps the file's ending.
	writeDiffFile(t, "nonl.txt", "a\nb")
	out, err = runDiff(t, `{"cmd":"apply","args":{"file":"nonl.txt","patch":"@@ -1,2 +1,2 @@\n a\n-b\n+c\n\\ No newline at end of file\n","dry_run":false}}`)
	if err != nil {
		t.Fatalf("%v %q", err, out)
	}
	if got, _ := os.ReadFile("nonl.txt"); string(got) != "a\nc" {
		t.Fatalf("no-newline ending lost: %q", got)
	}
}
