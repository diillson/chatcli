package workers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBatchReadScript(t *testing.T) {
	// Create temp dir with Go files
	dir := t.TempDir()
	writeTestFile(t, dir, "a.go", "package main\nfunc A() {}")
	writeTestFile(t, dir, "b.go", "package main\nfunc B() {}")

	result, err := batchReadScript(context.Background(), map[string]string{
		"dir":  dir,
		"glob": "*.go",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Batch Read") {
		t.Error("expected 'Batch Read' header in result")
	}
	if !strings.Contains(result, "a.go") {
		t.Error("expected a.go in result")
	}
	if !strings.Contains(result, "b.go") {
		t.Error("expected b.go in result")
	}
}

func TestBatchReadScript_NoFiles(t *testing.T) {
	dir := t.TempDir()

	result, err := batchReadScript(context.Background(), map[string]string{
		"dir":  dir,
		"glob": "*.go",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "No files matching") {
		t.Errorf("expected 'No files matching' message, got: %s", result)
	}
}

func TestBatchReadScript_Defaults(t *testing.T) {
	// Test with empty dir and glob (should use defaults "." and "*.go")
	result, err := batchReadScript(context.Background(), map[string]string{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should either find Go files in cwd or report "No files matching"
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestBatchReadScript_ContextCancelled(t *testing.T) {
	dir := t.TempDir()
	// Create many files to increase chance of hitting cancellation
	for i := 0; i < 10; i++ {
		writeTestFile(t, dir, filepath.Join("sub", "file"+string(rune('0'+i))+".go"), "package sub")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// Should not panic, may return partial or error results
	_, _ = batchReadScript(ctx, map[string]string{"dir": dir, "glob": "*.go"}, nil)
}

func TestBatchReadScript_RecursiveWalk(t *testing.T) {
	// Create nested dir structure so direct glob fails but walk finds files
	dir := t.TempDir()
	subDir := filepath.Join(dir, "pkg", "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, subDir, "deep.go", "package sub\nfunc Deep() {}")

	result, err := batchReadScript(context.Background(), map[string]string{
		"dir":  dir,
		"glob": "*.go",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "deep.go") {
		t.Errorf("expected deep.go found via recursive walk, got: %s", result)
	}
}

func TestMapProjectScript(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "main.go", `package main

type MyInterface interface {
	DoStuff()
}

type MyStruct struct {
	Field string
}

func Main() {}
`)

	result, err := mapProjectScript(context.Background(), map[string]string{"dir": dir}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Project Map") {
		t.Error("expected 'Project Map' header")
	}
}

func TestMapProjectScript_Defaults(t *testing.T) {
	result, err := mapProjectScript(context.Background(), map[string]string{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestSmartCommitScript(t *testing.T) {
	// smartCommitScript runs git-status and git-diff via engine.
	// It will work even outside a git repo (commands will error but script won't panic)
	result, err := smartCommitScript(context.Background(), map[string]string{}, nil)
	// err might be non-nil if not in a git repo, that's ok
	_ = err
	if result == "" {
		t.Error("expected non-empty result from smartCommitScript")
	}
	if !strings.Contains(result, "Status") {
		t.Error("expected 'Status' section in result")
	}
}

func TestReviewChangesScript(t *testing.T) {
	result, err := reviewChangesScript(context.Background(), map[string]string{}, nil)
	_ = err
	if result == "" {
		t.Error("expected non-empty result from reviewChangesScript")
	}
	if !strings.Contains(result, "Changed Files") {
		t.Error("expected 'Changed Files' section in result")
	}
	if !strings.Contains(result, "Recent Commits") {
		t.Error("expected 'Recent Commits' section in result")
	}
}

func TestRunTestsScript(t *testing.T) {
	// runTestsScript runs "test --dir ." via engine.
	// The engine's test command may fail, but the script should return structured output
	result, err := runTestsScript(context.Background(), map[string]string{"dir": "."}, nil)
	_ = err // test command may fail in this context
	if result == "" {
		t.Error("expected non-empty result")
	}
	if !strings.Contains(result, "Test Results") {
		t.Error("expected 'Test Results' header in output")
	}
}

func TestRunTestsScript_DefaultDir(t *testing.T) {
	result, err := runTestsScript(context.Background(), map[string]string{}, nil)
	_ = err
	if !strings.Contains(result, "Test Results") {
		t.Errorf("expected 'Test Results' header, got: %s", result)
	}
}

func TestBuildCheckScript(t *testing.T) {
	result, err := buildCheckScript(context.Background(), map[string]string{"dir": "."}, nil)
	_ = err
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestBuildCheckScript_DefaultDir(t *testing.T) {
	result, err := buildCheckScript(context.Background(), map[string]string{}, nil)
	_ = err
	if result == "" {
		t.Error("expected non-empty result")
	}
}

// writeTestFile creates a file in dir with the given name and content.
func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	fullPath := filepath.Join(dir, name)
	parent := filepath.Dir(fullPath)
	if err := os.MkdirAll(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
