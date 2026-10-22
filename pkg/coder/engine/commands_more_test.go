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

func TestHandleGitShowAndBlame(t *testing.T) {
	dir := newGitRepo(t)
	e, out := newGitTestEngine(dir)
	ctx := context.Background()

	if err := e.Execute(ctx, "git-show", []string{"--dir", dir}); err != nil {
		t.Fatalf("git-show: %v", err)
	}
	if s := out.String(); !strings.Contains(s, "initial commit") || !strings.Contains(s, "+hello") {
		t.Fatalf("show = %q", s)
	}
	out.Reset()
	if err := e.Execute(ctx, "git-show", []string{"--dir", dir, "--stat"}); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "tracked.txt") || strings.Contains(s, "+hello") {
		t.Fatalf("show --stat = %q", s)
	}
	out.Reset()
	if err := e.Execute(ctx, "git-show", []string{"--dir", dir, "--ref", "HEAD", "--path", "tracked.txt"}); err != nil {
		t.Fatal(err)
	}
	if s := strings.TrimSpace(out.String()); s != "hello" {
		t.Fatalf("show --path = %q", s)
	}
	if err := e.Execute(ctx, "git-show", []string{"--dir", dir, "--ref", "--output=/tmp/x"}); err == nil || !strings.Contains(err.Error(), "dash") {
		t.Fatalf("a ref that looks like an option must be refused: %v", err)
	}

	out.Reset()
	if err := e.Execute(ctx, "git-blame", []string{"--dir", dir, "--file", "tracked.txt"}); err != nil {
		t.Fatalf("git-blame: %v", err)
	}
	if s := out.String(); !strings.Contains(s, "test") || !strings.Contains(s, "hello") {
		t.Fatalf("blame = %q", s)
	}
	out.Reset()
	if err := e.Execute(ctx, "git-blame", []string{"--dir", dir, "--file", "tracked.txt", "--start", "1", "--end", "1"}); err != nil {
		t.Fatalf("blame range: %v", err)
	}
	if err := e.Execute(ctx, "git-blame", []string{"--dir", dir}); err == nil {
		t.Fatal("blame without --file must error")
	}
	if err := e.Execute(ctx, "git-blame", []string{"--dir", dir, "--file", "tracked.txt", "--start", "5", "--end", "2"}); err == nil {
		t.Fatal("inverted range must error")
	}
}

func TestHandleFindMoveDelete(t *testing.T) {
	e, out, root := newWsEngine(t)
	ctx := context.Background()
	mk := func(rel, content string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mk("a/one_test.go", "x")
	mk("a/one.go", "x")
	mk("b/two_test.go", "x")
	mk("node_modules/skip_test.go", "x")
	mk(".hidden/h_test.go", "x")

	if err := e.Execute(ctx, "find", []string{"--dir", root, "--pattern", "*_test.go"}); err != nil {
		t.Fatalf("find: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "a/one_test.go") || !strings.Contains(s, "b/two_test.go") || strings.Contains(s, "node_modules") || strings.Contains(s, ".hidden") || strings.Contains(s, "one.go\n") {
		t.Fatalf("find = %q", s)
	}
	out.Reset()
	if err := e.Execute(ctx, "find", []string{"--dir", root, "--contains", "B/TWO"}); err != nil || !strings.Contains(out.String(), "b/two_test.go") {
		t.Fatalf("find --contains: %q %v", out.String(), err)
	}
	out.Reset()
	if err := e.Execute(ctx, "find", []string{"--dir", root, "--pattern", "*_test.go", "--limit", "1"}); err != nil || !strings.Contains(out.String(), "[1 of 2 shown") {
		t.Fatalf("find limit: %q %v", out.String(), err)
	}
	out.Reset()
	if err := e.Execute(ctx, "find", []string{"--dir", root, "--pattern", "*.rs"}); err != nil || !strings.Contains(out.String(), "No file matches") {
		t.Fatalf("find none: %q %v", out.String(), err)
	}
	if err := e.Execute(ctx, "find", []string{"--dir", root}); err == nil {
		t.Fatal("find without pattern/contains must error")
	}
	if err := e.Execute(ctx, "find", []string{"--dir", root, "--pattern", "["}); err == nil {
		t.Fatal("invalid glob must error")
	}
	out.Reset()
	if err := e.Execute(ctx, "find", []string{"--dir", root, "--pattern", "a", "--dirs"}); err != nil || !strings.Contains(out.String(), "a\n") {
		t.Fatalf("find --dirs: %q %v", out.String(), err)
	}

	src := filepath.Join(root, "a", "one.go")
	dst := filepath.Join(root, "c", "renamed.go")
	if err := e.Execute(ctx, "move", []string{"--from", src, "--to", dst}); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatal("destination must exist")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source must be gone")
	}
	if _, err := os.Stat(src + ".bak"); err != nil {
		t.Fatal("a backup of the source must remain")
	}
	mk("c/other.go", "y")
	if err := e.Execute(ctx, "move", []string{"--from", dst, "--to", filepath.Join(root, "c", "other.go")}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("existing destination refused without --force: %v", err)
	}
	if err := e.Execute(ctx, "move", []string{"--from", dst, "--to", filepath.Join(root, "c", "other.go"), "--force"}); err != nil {
		t.Fatalf("move --force: %v", err)
	}
	if err := e.Execute(ctx, "move", []string{"--from", dst}); err == nil {
		t.Fatal("move without --to must error")
	}

	target := filepath.Join(root, "b", "two_test.go")
	if err := e.Execute(ctx, "delete", []string{"--file", target}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("file must be gone")
	}
	if data, err := os.ReadFile(target + ".bak"); err != nil || string(data) != "x" {
		t.Fatal("backup must hold the deleted content")
	}
	if err := e.Execute(ctx, "rollback", []string{"--file", target}); err != nil {
		t.Fatalf("rollback after delete: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("rollback must bring the file back")
	}
	if err := e.Execute(ctx, "delete", []string{"--file", filepath.Join(root, "a")}); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("delete of a directory refused: %v", err)
	}
	if err := e.Execute(ctx, "delete", []string{"--file", filepath.Join(root, "nope")}); err == nil {
		t.Fatal("delete of a missing file must error")
	}
	for _, c := range []string{"find", "move", "delete", "git-show", "git-blame"} {
		if !strings.Contains(GetMetadata().Usage, c) || !strings.Contains(GetSchema(), `"`+c+`"`) {
			t.Fatalf("%s missing from usage/schema", c)
		}
	}
}
