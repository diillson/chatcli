package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInit creates a throwaway git repo, returning its path. Callers are
// responsible for cleanup via t.Cleanup.
func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "base"},
		{"config", "user.email", "qg@test"},
		{"config", "user.name", "qg-test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func gitCommit(t *testing.T, dir, msg string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"add", "-A"},
		{"commit", "-q", "-m", msg},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// TestRun_EndToEnd builds a tiny git repo, commits a baseline, then a
// "feature" commit that adds a covered line and an uncovered line, and
// verifies qg-diffcover reports the right percent.
func TestRun_EndToEnd(t *testing.T) {
	repo := gitInit(t)

	// Baseline file with one function. The "old" function will be
	// untouched by the diff so it doesn't affect patch coverage.
	gitCommit(t, repo, "baseline", map[string]string{
		"foo.go": `package foo

func Old() int {
	return 1
}
`,
	})
	baselineCmd := exec.Command("git", "rev-parse", "HEAD")
	baselineCmd.Dir = repo
	baseline, _ := baselineCmd.Output()
	_ = baseline

	// "Feature" commit adds two new lines: line 6 (covered) and line 11
	// (uncovered). The cover profile reflects that split.
	gitCommit(t, repo, "feature", map[string]string{
		"foo.go": `package foo

func Old() int {
	return 1
}
func Covered() int {
	return 2
}
func Uncovered() int {
	return 3
}
`,
	})

	// Hand-crafted profile: line 7 (inside Covered) is hit, line 10
	// (inside Uncovered) is not. We claim the package path matches the
	// repo's empty module so the strip prefix is a no-op.
	profile := `mode: set
foo.go:6.16,8.2 1 1
foo.go:9.18,11.2 1 0
`
	profilePath := filepath.Join(repo, "cov.out")
	if err := os.WriteFile(profilePath, []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run the tool against base...HEAD. With one covered line and one
	// uncovered, percent should be 50% — under the default 60 threshold.
	var stdout, stderr bytes.Buffer
	prevDir, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevDir) })

	err := run([]string{
		"-coverage", "cov.out",
		"-base", "HEAD~1",
		"-threshold", "60",
		"-include", "*.go",
	}, &stdout, &stderr)

	if err == nil {
		t.Fatalf("expected non-nil error (50%% < 60%% threshold), got nil\nstdout: %s", stdout.String())
	}
	if !strings.Contains(err.Error(), "patch coverage") {
		t.Errorf("error should mention patch coverage, got: %v", err)
	}

	out := stdout.String()
	// The two new functions span 6 lines (3 each: signature, return, closing
	// brace) and the profile marks one function's block covered and the
	// other's uncovered, so the gate computes 3/6 = 50%.
	if !strings.Contains(out, "percent=50.0") {
		t.Errorf("expected percent=50.0 in output:\n%s", out)
	}
	if !strings.Contains(out, "covered=3") || !strings.Contains(out, "total=6") {
		t.Errorf("expected covered=3 total=6 in output:\n%s", out)
	}
}

func TestRun_PassWhenAboveThreshold(t *testing.T) {
	repo := gitInit(t)
	gitCommit(t, repo, "baseline", map[string]string{
		"foo.go": "package foo\n",
	})
	gitCommit(t, repo, "feature", map[string]string{
		"foo.go": `package foo

func Covered() int {
	return 42
}
`,
	})

	profile := `mode: set
foo.go:3.16,5.2 1 1
`
	if err := os.WriteFile(filepath.Join(repo, "cov.out"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}

	prevDir, _ := os.Getwd()
	_ = os.Chdir(repo)
	t.Cleanup(func() { _ = os.Chdir(prevDir) })

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"-coverage", "cov.out",
		"-base", "HEAD~1",
		"-threshold", "60",
		"-include", "*.go",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("expected pass, got %v\nstdout: %s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "percent=100.0") {
		t.Errorf("expected 100%% pass, got:\n%s", stdout.String())
	}
}

func TestRun_UninstrumentedFileFails(t *testing.T) {
	repo := gitInit(t)
	gitCommit(t, repo, "baseline", map[string]string{"foo.go": "package foo\n"})
	gitCommit(t, repo, "feature", map[string]string{
		"foo.go": `package foo

func A() int {
	return 1
}
`,
	})

	// Profile is empty (mode line only) — file in diff has zero entries.
	if err := os.WriteFile(filepath.Join(repo, "cov.out"), []byte("mode: set\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	prevDir, _ := os.Getwd()
	_ = os.Chdir(repo)
	t.Cleanup(func() { _ = os.Chdir(prevDir) })

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"-coverage", "cov.out",
		"-base", "HEAD~1",
		"-threshold", "60",
		"-include", "*.go",
	}, &stdout, &stderr)

	if err == nil {
		t.Fatalf("expected error for uninstrumented Go file")
	}
	if !strings.Contains(err.Error(), "uninstrumented") {
		t.Errorf("error should mention uninstrumented, got: %v", err)
	}
}

func TestRun_MarkdownOutputContainsTable(t *testing.T) {
	repo := gitInit(t)
	gitCommit(t, repo, "baseline", map[string]string{"foo.go": "package foo\n"})
	gitCommit(t, repo, "feature", map[string]string{
		"foo.go": `package foo

func A() int {
	return 1
}
func B() int {
	return 2
}
`,
	})

	profile := `mode: set
foo.go:3.10,5.2 1 1
foo.go:6.10,8.2 1 0
`
	if err := os.WriteFile(filepath.Join(repo, "cov.out"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}

	prevDir, _ := os.Getwd()
	_ = os.Chdir(repo)
	t.Cleanup(func() { _ = os.Chdir(prevDir) })

	mdPath := filepath.Join(repo, "report.md")
	var stdout, stderr bytes.Buffer
	_ = run([]string{
		"-coverage", "cov.out",
		"-base", "HEAD~1",
		"-threshold", "100",
		"-include", "*.go",
		"-markdown", mdPath,
	}, &stdout, &stderr)

	md, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("expected markdown file: %v", err)
	}
	if !strings.Contains(string(md), "Patch coverage") {
		t.Errorf("markdown missing summary: %s", md)
	}
	if !strings.Contains(string(md), "`foo.go`") {
		t.Errorf("markdown missing per-file row: %s", md)
	}
}
