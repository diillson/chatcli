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

// recordingCompressionAdapter keeps the content it was asked to compress so
// file reads and concatenation can be asserted.
type recordingCompressionAdapter struct {
	content string
	hint    string
}

func (r *recordingCompressionAdapter) Recall(string) (string, bool) { return "", false }
func (r *recordingCompressionAdapter) Compress(hint, content string) (string, error) {
	r.hint, r.content = hint, content
	return "CCR_KEY_1 compressed(" + content[:min(len(content), 12)] + ")", nil
}
func (r *recordingCompressionAdapter) Stats() string { return "stats" }

func TestCompressReadsFileAndFiles(t *testing.T) {
	rec := &recordingCompressionAdapter{}
	defer withAdapter(rec)()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.log"), filepath.Join(dir, "b.log")
	if err := os.WriteFile(a, []byte("alpha line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("beta line"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewBuiltinCompressPlugin()

	if _, err := p.Execute(context.Background(), []string{`{"file":"` + a + `","hint":"log"}`}); err != nil {
		t.Fatal(err)
	}
	if rec.content != "alpha line\n" || rec.hint != "log" {
		t.Fatalf("single file: content=%q hint=%q", rec.content, rec.hint)
	}

	if _, err := p.Execute(context.Background(), []string{`{"cmd":"compress","args":{"files":["` + a + `","` + b + `"]}}`}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rec.content, "===== "+a+" (11 bytes) =====\nalpha line\n===== "+b+" (9 bytes) =====\nbeta line\n") {
		t.Fatalf("files must be concatenated with headers: %q", rec.content)
	}

	// Comma-separated string form and the flattened argv the agent emits.
	if _, err := p.Execute(context.Background(), []string{`{"files":"` + a + `, ` + b + `"}`}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(rec.content, "=====") != 4 {
		t.Fatalf("csv files form: %q", rec.content)
	}
	if _, err := p.Execute(context.Background(), []string{"--file", a, "--hint", "log"}); err != nil {
		t.Fatal(err)
	}
	if rec.content != "alpha line\n" {
		t.Fatalf("flattened argv must read the file, got %q", rec.content)
	}
	// Inline content plus a file: both reach the compressor.
	if _, err := p.Execute(context.Background(), []string{`{"content":"inline","file":"` + a + `"}`}); err != nil {
		t.Fatal(err)
	}
	if rec.content != "inline\nalpha line\n" {
		t.Fatalf("content+file: %q", rec.content)
	}
}

func TestCompressFileLimitsAndErrors(t *testing.T) {
	rec := &recordingCompressionAdapter{}
	defer withAdapter(rec)()
	dir := t.TempDir()
	big := filepath.Join(dir, "big.log")
	if err := os.WriteFile(big, make([]byte, compressMaxFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewBuiltinCompressPlugin()
	cases := []struct{ args, want string }{
		{`{"file":"` + big + `"}`, "per-file limit"},
		{`{"file":"` + dir + `"}`, "is a directory"},
		{`{"file":"` + filepath.Join(dir, "missing.log") + `"}`, "no such file"},
		{`{"hint":"log"}`, `"content", "file" or "files" is required`},
	}
	for _, c := range cases {
		_, err := p.Execute(context.Background(), []string{c.args})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err=%v want %q", c.args, err, c.want)
		}
	}
	many := make([]string, compressMaxFiles+1)
	for i := range many {
		many[i] = `"x` + string(rune('a'+i%26)) + `"`
	}
	if _, err := p.Execute(context.Background(), []string{`{"files":[` + strings.Join(many, ",") + `]}`}); err == nil || !strings.Contains(err.Error(), "too many files") {
		t.Fatalf("file count must be bounded: %v", err)
	}
}

func TestCompressOutWritesAtomically(t *testing.T) {
	rec := &recordingCompressionAdapter{}
	defer withAdapter(rec)()
	dir := t.TempDir()
	out := filepath.Join(dir, "nested", "summary.txt")
	p := NewBuiltinCompressPlugin()
	res, err := p.Execute(context.Background(), []string{`{"content":"a very long payload to shrink","out":"` + out + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("out not written: %v", err)
	}
	if !strings.HasPrefix(string(data), "CCR_KEY_1") {
		t.Fatalf("out content: %q", data)
	}
	if !strings.Contains(res, "wrote the result to "+out) || !strings.Contains(res, "CCR_KEY_1") {
		t.Fatalf("summary must name the path and preview the key line: %q", res)
	}
	if entries, _ := os.ReadDir(filepath.Dir(out)); len(entries) != 1 {
		t.Fatalf("no temp files may remain: %v", entries)
	}
	if _, err := p.Execute(context.Background(), []string{`{"content":"x","out":"` + dir + `"}`}); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("out pointing at a directory must refuse: %v", err)
	}
}

func TestCompressCapsAndDescribeWithFiles(t *testing.T) {
	checkCaps(t, "@compress", NewBuiltinCompressPlugin(), []capsCase{
		{[]string{`{"file":"a.log"}`}, true, true},
		{[]string{`{"files":["a","b"]}`}, true, true},
		{[]string{`{"content":"x","out":"/tmp/o.txt"}`}, false, false},
		{[]string{"--file", "a.log", "--out", "o.txt"}, false, false},
		{[]string{`{bad json`}, false, false},
	})
	p := NewBuiltinCompressPlugin()
	if got := p.DescribeCall([]string{`{"file":"build.log"}`}); !strings.Contains(got, "build.log") {
		t.Fatalf("describe file: %q", got)
	}
	if got := p.DescribeCall([]string{`{"files":["a","b","c"]}`}); !strings.Contains(got, "describe.files") || !strings.Contains(got, "3") {
		t.Fatalf("describe files: %q", got)
	}
	if got := p.DescribeCall([]string{`{"cmd":"stats"}`}); !strings.Contains(got, "describe.stats") {
		t.Fatalf("describe stats: %q", got)
	}
	if !strings.Contains(p.Usage(), "files") || !strings.Contains(p.Schema(), `"out"`) {
		t.Fatal("Usage/Schema must document file, files and out")
	}
}
