package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBinary_PrintsTotalPercent builds the binary and runs it against a
// hand-crafted profile to confirm the stdout contract scripts depend on.
func TestBinary_PrintsTotalPercent(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "cov.out")
	in := "mode: atomic\n" +
		"a.go:1.1,2.2 4 1\n" + // 4 stmts covered
		"a.go:5.1,6.2 6 0\n" // 6 stmts uncovered  → 4/10 = 40.0
	if err := os.WriteFile(profile, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(dir, "qg-cover-total")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("go build: %v", err)
	}

	cmd := exec.Command(bin, "-profile", profile)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run binary: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != "40.0" {
		t.Errorf("stdout = %q, want %q", got, "40.0")
	}
}

func TestBinary_ExitsNonZeroOnMissingFile(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "qg-cover-total")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("go build: %v", err)
	}

	cmd := exec.Command(bin, "-profile", filepath.Join(dir, "does-not-exist.out"))
	if err := cmd.Run(); err == nil {
		t.Fatal("expected non-zero exit for missing profile")
	}
}
