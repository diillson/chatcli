package engine

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newGitRepo creates a temp git repo with one committed file and one
// untracked file, returning the repo dir. It skips the test if git is
// unavailable.
func newGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run("init")
	run("checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "tracked.txt")
	run("commit", "-m", "initial commit")

	// Untracked file for status/changed to report.
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newGitTestEngine() (*Engine, *bytes.Buffer) {
	var out bytes.Buffer
	e := NewEngine(&out, &out, "")
	return e, &out
}

func TestHandleGitStatus(t *testing.T) {
	dir := newGitRepo(t)
	e, out := newGitTestEngine()
	if err := e.Execute(context.Background(), "git-status", []string{"--dir", dir}); err != nil {
		t.Fatalf("git-status: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "main") {
		t.Errorf("status output missing branch:\n%s", s)
	}
	if !strings.Contains(s, "untracked.txt") {
		t.Errorf("status output missing untracked file:\n%s", s)
	}
}

func TestHandleGitBranch(t *testing.T) {
	dir := newGitRepo(t)
	e, out := newGitTestEngine()
	if err := e.Execute(context.Background(), "git-branch", []string{"--dir", dir}); err != nil {
		t.Fatalf("git-branch: %v", err)
	}
	if !strings.Contains(out.String(), "main") {
		t.Errorf("branch output = %q", out.String())
	}
}

func TestHandleGitLog(t *testing.T) {
	dir := newGitRepo(t)
	e, out := newGitTestEngine()
	if err := e.Execute(context.Background(), "git-log", []string{"--dir", dir, "--limit", "5"}); err != nil {
		t.Fatalf("git-log: %v", err)
	}
	if !strings.Contains(out.String(), "initial commit") {
		t.Errorf("log output = %q", out.String())
	}
}

func TestHandleGitDiff(t *testing.T) {
	dir := newGitRepo(t)
	// Modify the tracked file so diff has content.
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("hello\nworld\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e, out := newGitTestEngine()
	if err := e.Execute(context.Background(), "git-diff", []string{"--dir", dir, "--context", "1"}); err != nil {
		t.Fatalf("git-diff: %v", err)
	}
	if !strings.Contains(out.String(), "world") {
		t.Errorf("diff output = %q", out.String())
	}
}

func TestHandleGitDiff_NameOnlyAndStat(t *testing.T) {
	dir := newGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("hello\nchanged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e, out := newGitTestEngine()
	if err := e.Execute(context.Background(), "git-diff",
		[]string{"--dir", dir, "--name-only", "--stat", "--path", "tracked.txt"}); err != nil {
		t.Fatalf("git-diff: %v", err)
	}
	if !strings.Contains(out.String(), "tracked.txt") {
		t.Errorf("name-only diff output = %q", out.String())
	}
}

func TestHandleGitChanged(t *testing.T) {
	dir := newGitRepo(t)
	e, out := newGitTestEngine()
	if err := e.Execute(context.Background(), "git-changed", []string{"--dir", dir}); err != nil {
		t.Fatalf("git-changed: %v", err)
	}
	if !strings.Contains(out.String(), "untracked.txt") {
		t.Errorf("changed output = %q", out.String())
	}
}

func TestHandleGit_BadFlag(t *testing.T) {
	e, _ := newGitTestEngine()
	err := e.Execute(context.Background(), "git-status", []string{"--nonexistent-flag"})
	if err == nil {
		t.Error("expected error for unknown flag")
	}
}
