/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarSpec is one member of a test tarball; link marks a symlink entry.
type tarSpec struct{ name, body, link string }

func writeTestTarGz(t *testing.T, path string, specs []tarSpec) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for _, s := range specs {
		h := &tar.Header{Name: s.name, Mode: 0o644, Size: int64(len(s.body)), Typeflag: tar.TypeReg}
		if s.link != "" {
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, s.link, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(s.body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []interface{ Close() error }{tw, gw, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func writeTestZip(t *testing.T, path string, members []tarSpec) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, m := range members {
		w, err := zw.Create(m.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(m.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestArchive_ListReadAndExtract(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestZip(t, "a.zip", []tarSpec{{"README.md", "# hello\n", ""}, {"bin/blob", "x\x00y", ""}, {"dir/", "", ""}})
	writeTestTarGz(t, "b.tgz", []tarSpec{{"src/main.go", "package main\n", ""}, {"src/link", "", "main.go"}})
	p := NewBuiltinArchivePlugin()
	ctx := context.Background()

	out, err := p.Execute(ctx, []string{`{"cmd":"list","args":{"file":"a.zip"}}`})
	if err != nil || !strings.Contains(out, "3 entries") || !strings.Contains(out, "file") || !strings.Contains(out, " 8 README.md") || !strings.Contains(out, " 0 dir/") {
		t.Fatalf("list zip: %v\n%s", err, out)
	}
	out, err = p.Execute(ctx, []string{"list", "b.tgz", "--limit", "1"})
	if err != nil || !strings.Contains(out, "… and 1 more") || !strings.Contains(out, "2 entries") {
		t.Fatalf("list tgz limit: %v\n%s", err, out)
	}
	out, err = p.Execute(ctx, []string{`{"file":"b.tgz","entry":"src/main.go"}`})
	if err != nil || out != "package main\n" {
		t.Fatalf("read: %v %q", err, out)
	}
	if out, err = p.Execute(ctx, []string{"read", "--file", "a.zip", "--entry", "README.md", "--max_bytes", "4"}); err != nil || !strings.HasPrefix(out, "# he\n… truncated at 4 of 8 bytes") {
		t.Fatalf("read truncated: %v %q", err, out)
	}
	if out, err = p.Execute(ctx, []string{"read", "a.zip", "--entry", "bin/blob"}); err != nil || !strings.Contains(out, "binary content") {
		t.Fatalf("read binary: %v %q", err, out)
	}
	if _, err = p.Execute(ctx, []string{"read", "a.zip", "--entry", "nope"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("read missing: %v", err)
	}

	if out, err = p.Execute(ctx, []string{`{"cmd":"extract","args":{"file":"b.tgz","to":"out"}}`}); err != nil || !strings.Contains(out, "extracted 2 entries") {
		t.Fatalf("extract: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join("out", "src", "main.go")); err != nil || string(b) != "package main\n" {
		t.Fatalf("extracted file: %v %q", err, b)
	}
	if target, err := os.Readlink(filepath.Join("out", "src", "link")); err != nil || target != "main.go" {
		t.Fatalf("inside symlink must be kept: %v %q", err, target)
	}
	if _, err = p.Execute(ctx, []string{"extract", "b.tgz", "--to", "out"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("re-extract without overwrite: %v", err)
	}
	if _, err = p.Execute(ctx, []string{"extract", "b.tgz", "--to", "out", "--overwrite"}); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if _, err = p.Execute(ctx, []string{"extract", "b.tgz", "--to", "out2", "--max_entries", "1"}); err == nil || !strings.Contains(err.Error(), "max_entries") {
		t.Fatalf("entry limit: %v", err)
	}
	if _, err = p.Execute(ctx, []string{"extract", "a.zip", "--to", filepath.Join("..", "escape")}); err == nil {
		t.Fatal("destination outside the workspace must be refused")
	}
}

func TestArchive_ExtractRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	cases := []struct {
		name  string
		specs []tarSpec
		args  string
		want  string
	}{
		{"zip-slip", []tarSpec{{"../../evil", "x", ""}}, "", "zip-slip"},
		{"absolute", []tarSpec{{"/etc/evil", "x", ""}}, "", "absolute path"},
		{"symlink escape", []tarSpec{{"lnk", "", "../../outside"}}, "", "outside the destination"},
		{"symlink then write through it", []tarSpec{{"d", "", ".."}, {"d/evil", "x", ""}}, "", "outside the destination"},
		{"oversize", []tarSpec{{"big", strings.Repeat("a", 100), ""}}, `,"max_entry_bytes":10`, "max_entry_bytes"},
		{"total", []tarSpec{{"a", strings.Repeat("a", 30), ""}, {"b", strings.Repeat("b", 30), ""}}, `,"max_total_bytes":40`, "max_total_bytes"},
	}
	p := NewBuiltinArchivePlugin()
	for _, c := range cases {
		file := strings.ReplaceAll(c.name, " ", "_") + ".tar.gz"
		writeTestTarGz(t, file, c.specs)
		dest := "out_" + strings.ReplaceAll(c.name, " ", "_")
		_, err := p.Execute(context.Background(), []string{`{"cmd":"extract","args":{"file":"` + file + `","to":"` + dest + `"` + c.args + `}}`})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	// Nothing escaped the workspace root and no refused payload was written.
	if _, err := os.Stat(filepath.Join(root, "..", "evil")); err == nil {
		t.Fatal("zip-slip payload written outside the destination")
	}
	for _, leftover := range []string{filepath.Join("out_oversize", "big"), filepath.Join("out_symlink_then_write_through_it", "d", "evil")} {
		if _, err := os.Lstat(leftover); err == nil {
			t.Errorf("%s must not exist after a refused entry", leftover)
		}
	}
	zipSlip := filepath.Join(root, "slip.zip")
	writeTestZip(t, zipSlip, []tarSpec{{"ok.txt", "fine", ""}, {"../../evil.txt", "x", ""}})
	out, err := p.Execute(context.Background(), []string{"extract", "slip.zip", "--to", "zout"})
	if err == nil || !strings.Contains(err.Error(), "zip-slip") || !strings.Contains(out, "extracted 1 entries") {
		t.Fatalf("zip-slip in zip: %v\n%s", err, out)
	}
}

func TestArchive_CreateRoundTrip(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, f := range []string{"src/b.go", "src/a.go", "src/.git/HEAD", "README.md"} {
		if err := os.MkdirAll(filepath.Dir(f), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("content of "+f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := NewBuiltinArchivePlugin()
	ctx := context.Background()
	for _, file := range []string{"dist/bundle.zip", "bundle.tgz"} {
		if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
			t.Fatal(err)
		}
		out, err := p.Execute(ctx, []string{`{"cmd":"create","args":{"file":"` + file + `","paths":["src","README.md"]}}`})
		if err != nil || !strings.Contains(out, "3 entries") {
			t.Fatalf("create %s: %v\n%s", file, err, out)
		}
		out, err = p.Execute(ctx, []string{"list", file})
		if err != nil {
			t.Fatal(err)
		}
		if i, j, k := strings.Index(out, "README.md"), strings.Index(out, "src/a.go"), strings.Index(out, "src/b.go"); i < 0 || j < i || k < j || strings.Contains(out, ".git") {
			t.Fatalf("%s: entries must be sorted and .git excluded:\n%s", file, out)
		}
		if _, err = p.Execute(ctx, []string{"create", file, "--paths", "src"}); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("create over existing: %v", err)
		}
	}
	if _, err := p.Execute(ctx, []string{"create", "x.rar", "--paths", "src"}); err == nil {
		t.Fatal("unknown output format must be refused")
	}
	if _, err := p.Execute(ctx, []string{"create", "y.zip", "--paths", "src", "--base", "src/.git"}); err == nil || !strings.Contains(err.Error(), "outside base") {
		t.Fatalf("path outside base: %v", err)
	}
	if _, err := p.Execute(ctx, []string{"create", "z.zip", "--paths", filepath.Join("..", "..")}); err == nil {
		t.Fatal("source outside the workspace must be refused")
	}
}
