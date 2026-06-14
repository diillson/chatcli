/*
 * ChatCLI - completer i18n rendering tests (argless template regression).
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// TestGetContextNameSuggestions_NoMissingMarkers pins the user-visible side
// of the %!s(MISSING) regression: rich context-name descriptions must render
// real values for mode, file count and tags.
func TestGetContextNameSuggestions_NoMissingMarkers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	handler, err := NewContextHandler(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.GetManager().CreateContext(context.Background(), "ctx-uno", "demo", []string{dir}, "full", []string{"tag1"}, false); err != nil {
		t.Fatalf("CreateContext: %v", err)
	}
	c := &ChatCLI{contextHandler: handler, logger: zap.NewNop()}

	suggestions := c.getContextNameSuggestions()
	if len(suggestions) != 1 || suggestions[0].Text != "ctx-uno" {
		t.Fatalf("suggestions = %+v", suggestions)
	}
	desc := suggestions[0].Description
	if strings.Contains(desc, "MISSING") || strings.Contains(desc, "%!") {
		t.Fatalf("description carries format corruption: %q", desc)
	}
	for _, want := range []string{"full", "1", "tag1"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description missing %q: %q", want, desc)
		}
	}
}

// TestDescribeFlag_SynthesizedDescription covers the option-for fallback with
// a value list — both halves formatted through i18n.T, never an outer Sprintf.
func TestDescribeFlag_SynthesizedDescription(t *testing.T) {
	got := describeFlag("/context", "--bogus", contextModeValueSuggestions())
	if strings.Contains(got, "MISSING") || strings.Contains(got, "%!") {
		t.Fatalf("describeFlag corrupted: %q", got)
	}
	if !strings.Contains(got, "/context") || !strings.Contains(got, "full") {
		t.Errorf("describeFlag = %q, want command and values rendered", got)
	}
}
