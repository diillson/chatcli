package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandPathMentions_RealFile(t *testing.T) {
	// Create a temp file to reference
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "main.go")
	_ = os.WriteFile(tmpFile, []byte("package main"), 0o644)

	// Test with absolute path
	input := "olha esse arquivo @" + tmpFile
	result := expandPathMentions(input)
	if !strings.Contains(result, "@file "+tmpFile) {
		t.Errorf("expected @file expansion, got %q", result)
	}
}

func TestExpandPathMentions_RelativePath(t *testing.T) {
	// Create a file in CWD
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer func() { _ = os.Chdir(oldWd) }()

	_ = os.MkdirAll("src", 0o755)
	_ = os.WriteFile("src/main.go", []byte("package main"), 0o644)

	input := "analise @./src/main.go por favor"
	result := expandPathMentions(input)
	if !strings.Contains(result, "@file ./src/main.go") {
		t.Errorf("expected @file ./src/main.go, got %q", result)
	}
}

func TestExpandPathMentions_NonexistentPath(t *testing.T) {
	input := "olha @nonexistent/file.go"
	result := expandPathMentions(input)
	// Should NOT expand because the file doesn't exist
	if strings.Contains(result, "@file") {
		t.Errorf("should not expand nonexistent path, got %q", result)
	}
}

func TestExpandPathMentions_KnownCommands(t *testing.T) {
	// Known @ commands should not be expanded
	inputs := []string{
		"use @file main.go",
		"run @command ls",
		"use @coder read",
		"search @websearch golang",
	}
	for _, input := range inputs {
		result := expandPathMentions(input)
		if result != input {
			t.Errorf("known command should not be modified: %q -> %q", input, result)
		}
	}
}

func TestExpandPathMentions_NoPathSeparator(t *testing.T) {
	// @word without path separators should NOT be expanded
	input := "fale com @john sobre isso"
	result := expandPathMentions(input)
	if result != input {
		t.Errorf("plain @word should not be modified: %q -> %q", input, result)
	}
}
