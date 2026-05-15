/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestPkgsFromFiles(t *testing.T) {
	in := []string{
		"cli/cli.go",
		"cli/agent_mode.go",
		"llm/openai/openai_client.go",
		"main.go",
	}
	got := pkgsFromFiles(in)
	sort.Strings(got)
	want := []string{"cli", "llm/openai"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pkgs = %v, want %v", got, want)
	}
}

func TestLoadFiles_FromFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "list.txt")
	if err := os.WriteFile(p, []byte("a.go\n\n   b.go\nc.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadFiles(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"a.go", "b.go", "c.go"}) {
		t.Errorf("got %v", got)
	}
}

func TestDetectModulePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/foo\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prevDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevDir) })

	got, err := detectModulePath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "example.com/foo" {
		t.Errorf("module = %q, want example.com/foo", got)
	}
}
