package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func testLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

func TestLoadBootstrapContent_AllFiles(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("Be helpful"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "USER.md"), []byte("I prefer Go"), 0o644)

	bl := NewBootstrapLoader(dir, "", testLogger())
	content := bl.LoadBootstrapContent()

	if !strings.Contains(content, "Be helpful") {
		t.Error("expected SOUL.md content")
	}
	if !strings.Contains(content, "I prefer Go") {
		t.Error("expected USER.md content")
	}
}

func TestLoadBootstrapContent_Priority(t *testing.T) {
	workspace := t.TempDir()
	global := t.TempDir()
	_ = os.WriteFile(filepath.Join(workspace, "SOUL.md"), []byte("workspace soul"), 0o644)
	_ = os.WriteFile(filepath.Join(global, "SOUL.md"), []byte("global soul"), 0o644)

	bl := NewBootstrapLoader(workspace, global, testLogger())
	content, ok := bl.LoadFile("SOUL.md")
	if !ok {
		t.Fatal("expected to find SOUL.md")
	}
	if content != "workspace soul" {
		t.Errorf("expected workspace to override global, got %q", content)
	}
}

func TestLoadBootstrapContent_GlobalFallback(t *testing.T) {
	workspace := t.TempDir()
	global := t.TempDir()
	_ = os.WriteFile(filepath.Join(global, "USER.md"), []byte("global user"), 0o644)

	bl := NewBootstrapLoader(workspace, global, testLogger())
	content, ok := bl.LoadFile("USER.md")
	if !ok {
		t.Fatal("expected to find USER.md from global")
	}
	if content != "global user" {
		t.Errorf("expected 'global user', got %q", content)
	}
}

func TestLoadBootstrapContent_MissingFiles(t *testing.T) {
	bl := NewBootstrapLoader(t.TempDir(), t.TempDir(), testLogger())
	content := bl.LoadBootstrapContent()
	if content != "" {
		t.Errorf("expected empty content for missing files, got %q", content)
	}
}

func TestCacheInvalidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SOUL.md")
	_ = os.WriteFile(path, []byte("v1"), 0o644)

	bl := NewBootstrapLoader(dir, "", testLogger())
	c1, _ := bl.LoadFile("SOUL.md")
	if c1 != "v1" {
		t.Fatalf("expected 'v1', got %q", c1)
	}

	// Modify file (need to wait for mtime to change)
	time.Sleep(10 * time.Millisecond)
	_ = os.WriteFile(path, []byte("v2"), 0o644)

	c2, _ := bl.LoadFile("SOUL.md")
	if c2 != "v2" {
		t.Errorf("expected 'v2' after modification, got %q", c2)
	}
}

func TestIsStale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SOUL.md")
	_ = os.WriteFile(path, []byte("v1"), 0o644)

	bl := NewBootstrapLoader(dir, "", testLogger())
	bl.LoadFile("SOUL.md")

	if bl.IsStale() {
		t.Error("should not be stale immediately after load")
	}

	time.Sleep(10 * time.Millisecond)
	_ = os.WriteFile(path, []byte("v2"), 0o644)

	if !bl.IsStale() {
		t.Error("should be stale after file modification")
	}
}
