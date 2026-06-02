/*
 * ChatCLI - tests for BuildWorkspaceContextMode (memory push/pull modes).
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newModeBuilder(t *testing.T) *ContextBuilder {
	t.Helper()
	wsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsDir, "SOUL.md"), []byte("You are helpful"), 0o644); err != nil {
		t.Fatal(err)
	}
	bl := NewBootstrapLoader(wsDir, "", testLogger())
	ms := NewMemoryStore(t.TempDir(), testLogger())
	ms.Manager().ProcessExtraction(`## LONGTERM
- embed.FS needs '/' on Windows

## PROFILE_UPDATE
name=Edilson
role=SRE

## TOPICS
oauth, cache`)
	return NewContextBuilder(bl, ms, t.TempDir())
}

func TestBuildWorkspaceContextMode_Off(t *testing.T) {
	cb := newModeBuilder(t)
	out := cb.BuildWorkspaceContextMode(context.Background(), "q", nil, nil, "off", "")
	if !strings.Contains(out, "You are helpful") {
		t.Errorf("off mode must keep bootstrap: %q", out)
	}
	if strings.Contains(out, "Memory") || strings.Contains(out, "Edilson") {
		t.Errorf("off mode must not inject memory: %q", out)
	}
}

func TestBuildWorkspaceContextMode_Index(t *testing.T) {
	cb := newModeBuilder(t)
	out := cb.BuildWorkspaceContextMode(context.Background(), "q", nil, nil, "index", "PULL-HINT")
	if !strings.Contains(out, "You are helpful") {
		t.Errorf("index mode must keep bootstrap: %q", out)
	}
	if !strings.Contains(out, "# Memory Index") || !strings.Contains(out, "Edilson") {
		t.Errorf("index mode must inject the compact index: %q", out)
	}
	if !strings.Contains(out, "PULL-HINT") {
		t.Errorf("index mode must append the recall hint: %q", out)
	}
	// The index is a digest, not a dump: full fact bodies must NOT appear.
	if strings.Contains(out, "embed.FS needs") {
		t.Errorf("index mode leaked full fact bodies: %q", out)
	}
}

func TestBuildWorkspaceContextMode_FullKeepsBootstrap(t *testing.T) {
	cb := newModeBuilder(t)
	out := cb.BuildWorkspaceContextMode(context.Background(), "windows embed", []string{"windows", "embed"}, nil, "full", "")
	if !strings.Contains(out, "You are helpful") {
		t.Errorf("full mode must keep bootstrap: %q", out)
	}
	// full must not carry the index header (it uses the live retrieval path).
	if strings.Contains(out, "# Memory Index") {
		t.Errorf("full mode must not emit the compact index: %q", out)
	}
}
