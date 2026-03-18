package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadWriteLongTerm(t *testing.T) {
	dir := t.TempDir()
	ms := NewMemoryStore(dir, testLogger())

	if err := ms.WriteLongTerm("# Key Facts\n- Go project"); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	content := ms.ReadLongTerm()
	if !strings.Contains(content, "Go project") {
		t.Errorf("expected 'Go project', got %q", content)
	}
}

func TestAppendLongTerm(t *testing.T) {
	dir := t.TempDir()
	ms := NewMemoryStore(dir, testLogger())

	_ = ms.WriteLongTerm("line1")
	_ = ms.AppendLongTerm("line2")

	content := ms.ReadLongTerm()
	if !strings.Contains(content, "line1") || !strings.Contains(content, "line2") {
		t.Errorf("expected both lines, got %q", content)
	}
}

func TestReadLongTerm_NoFile(t *testing.T) {
	ms := NewMemoryStore(t.TempDir(), testLogger())
	content := ms.ReadLongTerm()
	if content != "" {
		t.Errorf("expected empty string, got %q", content)
	}
}

func TestWriteDailyNote(t *testing.T) {
	dir := t.TempDir()
	ms := NewMemoryStore(dir, testLogger())

	if err := ms.WriteDailyNote("Test entry"); err != nil {
		t.Fatalf("write daily note failed: %v", err)
	}

	path := ms.TodayNotePath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read daily note: %v", err)
	}
	if !strings.Contains(string(data), "Test entry") {
		t.Error("expected 'Test entry' in daily note")
	}
}

func TestTodayNotePath(t *testing.T) {
	dir := t.TempDir()
	ms := NewMemoryStore(dir, testLogger())

	path := ms.TodayNotePath()
	now := time.Now()
	expectedDir := now.Format("200601")
	expectedFile := now.Format("20060102") + ".md"

	if !strings.Contains(path, expectedDir) {
		t.Errorf("expected YYYYMM dir in path, got %q", path)
	}
	if !strings.HasSuffix(path, expectedFile) {
		t.Errorf("expected YYYYMMDD.md filename, got %q", filepath.Base(path))
	}
}

func TestGetRecentDailyNotes(t *testing.T) {
	dir := t.TempDir()
	ms := NewMemoryStore(dir, testLogger())

	// Create today's note
	_ = ms.WriteDailyNote("today's entry")

	notes := ms.GetRecentDailyNotes(3)
	if len(notes) != 1 {
		t.Errorf("expected 1 note, got %d", len(notes))
	}
	if len(notes) > 0 && !strings.Contains(notes[0].Content, "today's entry") {
		t.Error("expected today's entry in notes")
	}
}

func TestGetMemoryContext(t *testing.T) {
	dir := t.TempDir()
	ms := NewMemoryStore(dir, testLogger())

	_ = ms.WriteLongTerm("# Memory\n- Important fact")
	_ = ms.WriteDailyNote("Did something today")

	ctx := ms.GetMemoryContext()
	if !strings.Contains(ctx, "Long-term Memory") {
		t.Error("expected 'Long-term Memory' section")
	}
	if !strings.Contains(ctx, "Important fact") {
		t.Error("expected memory content")
	}
	if !strings.Contains(ctx, "Recent Activity") && !strings.Contains(ctx, "Daily Notes") {
		t.Error("expected 'Recent Activity' or 'Daily Notes' section")
	}
}

func TestGetMemoryContext_Empty(t *testing.T) {
	ms := NewMemoryStore(t.TempDir(), testLogger())
	ctx := ms.GetMemoryContext()
	if ctx != "" {
		t.Errorf("expected empty context, got %q", ctx)
	}
}

func TestEnsureDirectories(t *testing.T) {
	dir := t.TempDir()
	ms := NewMemoryStore(dir, testLogger())

	if err := ms.EnsureDirectories(); err != nil {
		t.Fatalf("ensure dirs failed: %v", err)
	}

	memDir := filepath.Join(dir, "memory")
	if _, err := os.Stat(memDir); err != nil {
		t.Errorf("memory dir not created: %v", err)
	}
}
