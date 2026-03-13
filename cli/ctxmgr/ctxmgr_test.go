/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// newTestManager creates a Manager backed by a temp directory for isolation.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	logger := zap.NewNop()

	tmpDir := t.TempDir()

	storage := &Storage{
		basePath: tmpDir,
		logger:   logger,
	}

	return &Manager{
		contexts:         make(map[string]*FileContext),
		attachedContexts: make(map[string][]AttachedContext),
		Storage:          storage,
		validator:        NewValidator(logger),
		processor:        NewProcessor(logger),
		logger:           logger,
	}
}

// addTestContext is a helper that directly inserts a FileContext into the manager
// and persists it to disk, bypassing ProcessPaths (which needs real files).
func addTestContext(t *testing.T, m *Manager, name string, files []utils.FileInfo, mode ProcessingMode, tags []string) *FileContext {
	t.Helper()

	var totalSize int64
	for _, f := range files {
		totalSize += f.Size
	}

	ctx := &FileContext{
		ID:          name + "-id",
		Name:        name,
		Description: "test context: " + name,
		Files:       files,
		Mode:        mode,
		TotalSize:   totalSize,
		FileCount:   len(files),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		Tags:        tags,
		Metadata:    map[string]string{},
	}

	m.contexts[ctx.ID] = ctx
	if err := m.Storage.SaveContext(ctx); err != nil {
		t.Fatalf("failed to save test context: %v", err)
	}
	return ctx
}

func sampleFiles() []utils.FileInfo {
	return []utils.FileInfo{
		{Path: "src/main.go", Content: "package main\nfunc main() {}", Size: 30, Type: "Go"},
		{Path: "src/util.go", Content: "package main\nfunc helper() {}", Size: 32, Type: "Go"},
		{Path: "README.md", Content: "# Project", Size: 10, Type: "Markdown"},
	}
}

// ---------------------------------------------------------------------------
// Validator tests
// ---------------------------------------------------------------------------

func TestValidateName(t *testing.T) {
	v := NewValidator(zap.NewNop())

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "my-context", false},
		{"valid with spaces", "my context", false},
		{"valid with underscore", "my_context_1", false},
		{"too short", "ab", true},
		{"too long", strings.Repeat("a", MaxContextNameLength+1), true},
		{"empty", "", true},
		{"special chars", "ctx@#!", true},
		{"only spaces", "   ", true},
		{"only dashes", "---", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := v.ValidateName(tc.input)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateName(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
		})
	}
}

func TestValidateDescription(t *testing.T) {
	v := NewValidator(zap.NewNop())

	if err := v.ValidateDescription("short desc"); err != nil {
		t.Errorf("expected no error for short description, got %v", err)
	}

	longDesc := strings.Repeat("x", MaxDescriptionLength+1)
	if err := v.ValidateDescription(longDesc); err == nil {
		t.Error("expected error for description exceeding max length")
	}
}

func TestValidateTotalSize(t *testing.T) {
	v := NewValidator(zap.NewNop())

	if err := v.ValidateTotalSize(1024); err != nil {
		t.Errorf("expected no error for valid size, got %v", err)
	}
	if err := v.ValidateTotalSize(0); err == nil {
		t.Error("expected error for zero size")
	}
	if err := v.ValidateTotalSize(-1); err == nil {
		t.Error("expected error for negative size")
	}
	if err := v.ValidateTotalSize(MaxTotalSizeBytes + 1); err == nil {
		t.Error("expected error for size exceeding max")
	}
}

func TestValidateTags(t *testing.T) {
	v := NewValidator(zap.NewNop())

	if err := v.ValidateTags([]string{"go", "backend"}); err != nil {
		t.Errorf("expected no error for valid tags, got %v", err)
	}
	if err := v.ValidateTags([]string{"x"}); err == nil {
		t.Error("expected error for tag too short")
	}
	if err := v.ValidateTags([]string{strings.Repeat("a", 21)}); err == nil {
		t.Error("expected error for tag too long")
	}
	if err := v.ValidateTags([]string{"bad tag!"}); err == nil {
		t.Error("expected error for tag with invalid chars")
	}

	tooMany := make([]string, 11)
	for i := range tooMany {
		tooMany[i] = "tag"
	}
	if err := v.ValidateTags(tooMany); err == nil {
		t.Error("expected error for too many tags")
	}
}

func TestValidateMode(t *testing.T) {
	v := NewValidator(zap.NewNop())

	for _, mode := range []ProcessingMode{ModeFull, ModeSummary, ModeChunked, ModeSmart} {
		if err := v.ValidateMode(mode); err != nil {
			t.Errorf("expected no error for mode %s, got %v", mode, err)
		}
	}

	if err := v.ValidateMode("invalid"); err == nil {
		t.Error("expected error for invalid mode")
	}
}

func TestValidatePriority(t *testing.T) {
	v := NewValidator(zap.NewNop())

	if err := v.ValidatePriority(0); err != nil {
		t.Errorf("expected no error for priority 0, got %v", err)
	}
	if err := v.ValidatePriority(500); err != nil {
		t.Errorf("expected no error for priority 500, got %v", err)
	}
	if err := v.ValidatePriority(-1); err == nil {
		t.Error("expected error for negative priority")
	}
	if err := v.ValidatePriority(1001); err == nil {
		t.Error("expected error for priority exceeding max")
	}
}

func TestValidateContext(t *testing.T) {
	v := NewValidator(zap.NewNop())

	good := &FileContext{
		Name:      "valid-ctx",
		Mode:      ModeFull,
		TotalSize: 1024,
		FileCount: 2,
		Tags:      []string{"go"},
	}
	result := v.ValidateContext(good)
	if !result.Valid {
		t.Errorf("expected valid context, got errors: %v", result.Errors)
	}

	bad := &FileContext{
		Name:      "x",
		Mode:      "nope",
		TotalSize: 0,
		FileCount: 0,
	}
	result = v.ValidateContext(bad)
	if result.Valid {
		t.Error("expected invalid context")
	}
	if len(result.Errors) == 0 {
		t.Error("expected errors to be reported")
	}
}

func TestValidateContext_Warnings(t *testing.T) {
	v := NewValidator(zap.NewNop())

	big := &FileContext{
		Name:      "big-ctx",
		Mode:      ModeFull,
		TotalSize: 60 * 1024 * 1024, // 60MB
		FileCount: 600,
	}
	result := v.ValidateContext(big)
	if len(result.Warnings) == 0 {
		t.Error("expected warnings for large context")
	}
}

// ---------------------------------------------------------------------------
// Storage tests
// ---------------------------------------------------------------------------

func TestStorageSaveLoadDelete(t *testing.T) {
	logger := zap.NewNop()
	tmpDir := t.TempDir()
	storage := &Storage{basePath: tmpDir, logger: logger}

	ctx := &FileContext{
		ID:          "test-save-123",
		Name:        "save-test",
		Description: "testing persistence",
		Files:       sampleFiles(),
		Mode:        ModeFull,
		TotalSize:   72,
		FileCount:   3,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		Tags:        []string{"go"},
		Metadata:    map[string]string{"key": "val"},
	}

	// Save
	if err := storage.SaveContext(ctx); err != nil {
		t.Fatalf("SaveContext failed: %v", err)
	}

	// Verify file exists
	expectedPath := filepath.Join(tmpDir, "test-save-123.json")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Fatal("expected context file on disk")
	}

	// Load
	loaded, err := storage.LoadContext("test-save-123")
	if err != nil {
		t.Fatalf("LoadContext failed: %v", err)
	}
	if loaded.Name != ctx.Name {
		t.Errorf("Name mismatch: got %q, want %q", loaded.Name, ctx.Name)
	}
	if loaded.FileCount != ctx.FileCount {
		t.Errorf("FileCount mismatch: got %d, want %d", loaded.FileCount, ctx.FileCount)
	}

	// Delete
	if err := storage.DeleteContext("test-save-123"); err != nil {
		t.Fatalf("DeleteContext failed: %v", err)
	}
	if _, err := os.Stat(expectedPath); !os.IsNotExist(err) {
		t.Error("expected context file to be removed after delete")
	}
}

func TestStorageLoadAllContexts(t *testing.T) {
	logger := zap.NewNop()
	tmpDir := t.TempDir()
	storage := &Storage{basePath: tmpDir, logger: logger}

	for _, id := range []string{"ctx-a", "ctx-b", "ctx-c"} {
		ctx := &FileContext{
			ID:        id,
			Name:      id,
			Mode:      ModeFull,
			TotalSize: 10,
			FileCount: 1,
			Files:     []utils.FileInfo{{Path: "f.go", Content: "x", Size: 10, Type: "Go"}},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Metadata:  map[string]string{},
		}
		if err := storage.SaveContext(ctx); err != nil {
			t.Fatalf("SaveContext(%s) failed: %v", id, err)
		}
	}

	all, err := storage.LoadAllContexts()
	if err != nil {
		t.Fatalf("LoadAllContexts failed: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 contexts, got %d", len(all))
	}
}

func TestStorageLoadContext_NotFound(t *testing.T) {
	logger := zap.NewNop()
	tmpDir := t.TempDir()
	storage := &Storage{basePath: tmpDir, logger: logger}

	_, err := storage.LoadContext("nonexistent")
	if err == nil {
		t.Error("expected error loading nonexistent context")
	}
}

func TestStorageExportImport(t *testing.T) {
	logger := zap.NewNop()
	tmpDir := t.TempDir()
	storage := &Storage{basePath: tmpDir, logger: logger}

	ctx := &FileContext{
		ID:        "export-test",
		Name:      "export-ctx",
		Mode:      ModeFull,
		TotalSize: 10,
		FileCount: 1,
		Files:     []utils.FileInfo{{Path: "a.go", Content: "pkg", Size: 10, Type: "Go"}},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata:  map[string]string{},
	}

	exportPath := filepath.Join(t.TempDir(), "exported.json")
	if err := storage.ExportContext(ctx, exportPath); err != nil {
		t.Fatalf("ExportContext failed: %v", err)
	}

	imported, err := storage.ImportContext(exportPath)
	if err != nil {
		t.Fatalf("ImportContext failed: %v", err)
	}
	if imported.Name != ctx.Name {
		t.Errorf("imported name mismatch: got %q, want %q", imported.Name, ctx.Name)
	}

	// Should also be loadable from storage now
	loaded, err := storage.LoadContext(imported.ID)
	if err != nil {
		t.Fatalf("LoadContext after import failed: %v", err)
	}
	if loaded.ID != ctx.ID {
		t.Errorf("loaded ID mismatch after import")
	}
}

func TestStorageGetStoragePath(t *testing.T) {
	tmpDir := t.TempDir()
	storage := &Storage{basePath: tmpDir, logger: zap.NewNop()}
	if storage.GetStoragePath() != tmpDir {
		t.Errorf("GetStoragePath returned %q, want %q", storage.GetStoragePath(), tmpDir)
	}
}

// ---------------------------------------------------------------------------
// Manager tests: GetContext, GetContextByName, ListContexts
// ---------------------------------------------------------------------------

func TestManagerGetContext(t *testing.T) {
	m := newTestManager(t)
	ctx := addTestContext(t, m, "test-get", sampleFiles(), ModeFull, nil)

	got, err := m.GetContext(ctx.ID)
	if err != nil {
		t.Fatalf("GetContext failed: %v", err)
	}
	if got.Name != "test-get" {
		t.Errorf("name mismatch: got %q", got.Name)
	}
}

func TestManagerGetContext_NotFound(t *testing.T) {
	m := newTestManager(t)
	_, err := m.GetContext("no-such-id")
	if err == nil {
		t.Error("expected error for missing context")
	}
}

func TestManagerGetContextByName(t *testing.T) {
	m := newTestManager(t)
	addTestContext(t, m, "by-name", sampleFiles(), ModeFull, nil)

	got, err := m.GetContextByName("by-name")
	if err != nil {
		t.Fatalf("GetContextByName failed: %v", err)
	}
	if got.Name != "by-name" {
		t.Errorf("name mismatch: got %q", got.Name)
	}
}

func TestManagerGetContextByName_NotFound(t *testing.T) {
	m := newTestManager(t)
	_, err := m.GetContextByName("nope")
	if err == nil {
		t.Error("expected error for missing context name")
	}
}

func TestManagerListContexts_NoFilter(t *testing.T) {
	m := newTestManager(t)
	addTestContext(t, m, "list-a", sampleFiles(), ModeFull, []string{"go"})
	addTestContext(t, m, "list-b", sampleFiles(), ModeSummary, []string{"py"})

	all, err := m.ListContexts(nil)
	if err != nil {
		t.Fatalf("ListContexts failed: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 contexts, got %d", len(all))
	}
}

func TestManagerListContexts_FilterByMode(t *testing.T) {
	m := newTestManager(t)
	addTestContext(t, m, "mode-full", sampleFiles(), ModeFull, nil)
	addTestContext(t, m, "mode-summary", sampleFiles(), ModeSummary, nil)

	filtered, err := m.ListContexts(&ContextFilter{Mode: ModeFull})
	if err != nil {
		t.Fatalf("ListContexts with mode filter failed: %v", err)
	}
	if len(filtered) != 1 {
		t.Errorf("expected 1 context with mode full, got %d", len(filtered))
	}
	if filtered[0].Name != "mode-full" {
		t.Errorf("expected mode-full context, got %q", filtered[0].Name)
	}
}

func TestManagerListContexts_FilterByTags(t *testing.T) {
	m := newTestManager(t)
	addTestContext(t, m, "tagged", sampleFiles(), ModeFull, []string{"backend"})
	addTestContext(t, m, "untagged", sampleFiles(), ModeFull, nil)

	filtered, err := m.ListContexts(&ContextFilter{Tags: []string{"backend"}})
	if err != nil {
		t.Fatalf("ListContexts with tag filter failed: %v", err)
	}
	if len(filtered) != 1 {
		t.Errorf("expected 1 tagged context, got %d", len(filtered))
	}
}

func TestManagerListContexts_FilterBySize(t *testing.T) {
	m := newTestManager(t)
	// sampleFiles total = 72 bytes
	addTestContext(t, m, "small-ctx", sampleFiles(), ModeFull, nil)

	big := []utils.FileInfo{{Path: "big.bin", Content: strings.Repeat("x", 1000), Size: 1000, Type: "Binary"}}
	addTestContext(t, m, "big-ctx", big, ModeFull, nil)

	filtered, err := m.ListContexts(&ContextFilter{MinSize: 500})
	if err != nil {
		t.Fatalf("ListContexts with size filter failed: %v", err)
	}
	if len(filtered) != 1 {
		t.Errorf("expected 1 context with size >= 500, got %d", len(filtered))
	}
	if filtered[0].Name != "big-ctx" {
		t.Errorf("expected big-ctx, got %q", filtered[0].Name)
	}
}

func TestManagerListContexts_FilterByNamePattern(t *testing.T) {
	m := newTestManager(t)
	addTestContext(t, m, "api-service", sampleFiles(), ModeFull, nil)
	addTestContext(t, m, "web-client", sampleFiles(), ModeFull, nil)

	filtered, err := m.ListContexts(&ContextFilter{NamePattern: "^api"})
	if err != nil {
		t.Fatalf("ListContexts with name pattern failed: %v", err)
	}
	if len(filtered) != 1 {
		t.Errorf("expected 1 matching context, got %d", len(filtered))
	}
}

func TestManagerListContexts_FilterByDate(t *testing.T) {
	m := newTestManager(t)
	ctx := addTestContext(t, m, "dated", sampleFiles(), ModeFull, nil)
	// manually set CreatedAt to the past
	ctx.CreatedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	after := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	filtered, err := m.ListContexts(&ContextFilter{CreatedAfter: &after})
	if err != nil {
		t.Fatalf("ListContexts with date filter failed: %v", err)
	}
	if len(filtered) != 0 {
		t.Errorf("expected 0 contexts created after 2025, got %d", len(filtered))
	}
}

// ---------------------------------------------------------------------------
// Manager tests: DeleteContext
// ---------------------------------------------------------------------------

func TestManagerDeleteContext(t *testing.T) {
	m := newTestManager(t)
	ctx := addTestContext(t, m, "to-delete", sampleFiles(), ModeFull, nil)

	if err := m.DeleteContext(ctx.ID); err != nil {
		t.Fatalf("DeleteContext failed: %v", err)
	}

	_, err := m.GetContext(ctx.ID)
	if err == nil {
		t.Error("expected error after deletion")
	}
}

func TestManagerDeleteContext_NotFound(t *testing.T) {
	m := newTestManager(t)
	err := m.DeleteContext("nonexistent-id")
	if err == nil {
		t.Error("expected error deleting nonexistent context")
	}
}

func TestManagerDeleteContext_WhileAttached(t *testing.T) {
	m := newTestManager(t)
	ctx := addTestContext(t, m, "attached-del", sampleFiles(), ModeFull, nil)

	if err := m.AttachContext("session-1", ctx.ID, 0); err != nil {
		t.Fatalf("AttachContext failed: %v", err)
	}

	err := m.DeleteContext(ctx.ID)
	if err == nil {
		t.Error("expected error deleting context that is attached to a session")
	}
}

// ---------------------------------------------------------------------------
// Manager tests: AttachContext / DetachContext
// ---------------------------------------------------------------------------

func TestManagerAttachAndDetach(t *testing.T) {
	m := newTestManager(t)
	ctx := addTestContext(t, m, "attach-test", sampleFiles(), ModeFull, nil)

	sessionID := "session-abc"

	// Attach
	if err := m.AttachContext(sessionID, ctx.ID, 1); err != nil {
		t.Fatalf("AttachContext failed: %v", err)
	}

	attached, err := m.GetAttachedContexts(sessionID)
	if err != nil {
		t.Fatalf("GetAttachedContexts failed: %v", err)
	}
	if len(attached) != 1 {
		t.Fatalf("expected 1 attached context, got %d", len(attached))
	}
	if attached[0].ID != ctx.ID {
		t.Errorf("attached context ID mismatch")
	}

	// Detach
	if err := m.DetachContext(sessionID, ctx.ID); err != nil {
		t.Fatalf("DetachContext failed: %v", err)
	}

	attached, _ = m.GetAttachedContexts(sessionID)
	if len(attached) != 0 {
		t.Errorf("expected 0 attached contexts after detach, got %d", len(attached))
	}
}

func TestManagerAttachContext_NotFound(t *testing.T) {
	m := newTestManager(t)
	err := m.AttachContext("session", "bad-id", 0)
	if err == nil {
		t.Error("expected error attaching nonexistent context")
	}
}

func TestManagerAttachContext_Duplicate(t *testing.T) {
	m := newTestManager(t)
	ctx := addTestContext(t, m, "dup-attach", sampleFiles(), ModeFull, nil)

	if err := m.AttachContext("sess", ctx.ID, 0); err != nil {
		t.Fatalf("first attach failed: %v", err)
	}
	err := m.AttachContext("sess", ctx.ID, 0)
	if err == nil {
		t.Error("expected error attaching same context twice")
	}
}

func TestManagerDetachContext_NoAttachments(t *testing.T) {
	m := newTestManager(t)
	err := m.DetachContext("empty-session", "some-id")
	if err == nil {
		t.Error("expected error detaching from session with no attachments")
	}
}

func TestManagerDetachContext_NotAttached(t *testing.T) {
	m := newTestManager(t)
	ctx := addTestContext(t, m, "not-attached", sampleFiles(), ModeFull, nil)

	// Attach one context, then try to detach a different one
	if err := m.AttachContext("sess", ctx.ID, 0); err != nil {
		t.Fatalf("AttachContext failed: %v", err)
	}

	err := m.DetachContext("sess", "different-id")
	if err == nil {
		t.Error("expected error detaching context that is not attached")
	}
}

func TestManagerAttachContextWithOptions(t *testing.T) {
	m := newTestManager(t)
	ctx := addTestContext(t, m, "opts-attach", sampleFiles(), ModeFull, nil)

	opts := AttachOptions{
		Priority:       5,
		SelectedChunks: []int{1, 3},
	}

	if err := m.AttachContextWithOptions("sess", ctx.ID, opts); err != nil {
		t.Fatalf("AttachContextWithOptions failed: %v", err)
	}

	// Verify the attachment is stored
	m.mu.RLock()
	attachments := m.attachedContexts["sess"]
	m.mu.RUnlock()

	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if attachments[0].Priority != 5 {
		t.Errorf("priority mismatch: got %d, want 5", attachments[0].Priority)
	}
	if len(attachments[0].SelectedChunks) != 2 {
		t.Errorf("expected 2 selected chunks, got %d", len(attachments[0].SelectedChunks))
	}
}

func TestManagerAttachPriorityOrdering(t *testing.T) {
	m := newTestManager(t)
	ctxA := addTestContext(t, m, "prio-high", sampleFiles(), ModeFull, nil)
	ctxB := addTestContext(t, m, "prio-low", sampleFiles(), ModeFull, nil)

	// Attach B (priority 10) then A (priority 1)
	if err := m.AttachContext("sess", ctxB.ID, 10); err != nil {
		t.Fatal(err)
	}
	if err := m.AttachContext("sess", ctxA.ID, 1); err != nil {
		t.Fatal(err)
	}

	m.mu.RLock()
	attachments := m.attachedContexts["sess"]
	m.mu.RUnlock()

	if len(attachments) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(attachments))
	}
	// Should be sorted by priority ascending
	if attachments[0].ContextID != ctxA.ID {
		t.Errorf("expected lower priority context first, got context %s", attachments[0].ContextID)
	}
}

// ---------------------------------------------------------------------------
// Manager tests: MergeContexts
// ---------------------------------------------------------------------------

func TestManagerMergeContexts(t *testing.T) {
	m := newTestManager(t)
	ctxA := addTestContext(t, m, "merge-a", []utils.FileInfo{
		{Path: "a.go", Content: "a", Size: 1, Type: "Go"},
	}, ModeFull, nil)
	ctxB := addTestContext(t, m, "merge-b", []utils.FileInfo{
		{Path: "b.go", Content: "b", Size: 1, Type: "Go"},
	}, ModeFull, nil)

	merged, err := m.MergeContexts("merged-ctx", "merged", []string{ctxA.ID, ctxB.ID}, MergeOptions{
		Tags: []string{"merged"},
	})
	if err != nil {
		t.Fatalf("MergeContexts failed: %v", err)
	}
	if merged.FileCount != 2 {
		t.Errorf("expected 2 files in merged context, got %d", merged.FileCount)
	}
	if merged.Mode != ModeFull {
		t.Errorf("expected merged mode to be full, got %s", merged.Mode)
	}
}

func TestManagerMergeContexts_RemoveDuplicates(t *testing.T) {
	m := newTestManager(t)
	sharedFile := utils.FileInfo{Path: "shared.go", Content: "v1", Size: 2, Type: "Go"}

	ctxA := addTestContext(t, m, "dup-a", []utils.FileInfo{sharedFile}, ModeFull, nil)
	ctxB := addTestContext(t, m, "dup-b", []utils.FileInfo{
		{Path: "shared.go", Content: "v2-bigger", Size: 10, Type: "Go"},
	}, ModeFull, nil)

	merged, err := m.MergeContexts("deduped", "dedup test", []string{ctxA.ID, ctxB.ID}, MergeOptions{
		RemoveDuplicates: true,
		PreferNewer:      true,
	})
	if err != nil {
		t.Fatalf("MergeContexts with dedup failed: %v", err)
	}
	if merged.FileCount != 1 {
		t.Errorf("expected 1 file after dedup, got %d", merged.FileCount)
	}
	// With PreferNewer (uses size heuristic), should keep the bigger file
	if merged.Files[0].Size != 10 {
		t.Errorf("expected bigger file (size 10), got size %d", merged.Files[0].Size)
	}
}

func TestManagerMergeContexts_TooFewContexts(t *testing.T) {
	m := newTestManager(t)
	ctx := addTestContext(t, m, "solo", sampleFiles(), ModeFull, nil)

	_, err := m.MergeContexts("no-merge", "", []string{ctx.ID}, MergeOptions{})
	if err == nil {
		t.Error("expected error when merging fewer than 2 contexts")
	}
}

func TestManagerMergeContexts_DuplicateName(t *testing.T) {
	m := newTestManager(t)
	ctxA := addTestContext(t, m, "existing-name", sampleFiles(), ModeFull, nil)
	ctxB := addTestContext(t, m, "another-ctx", sampleFiles(), ModeFull, nil)

	_, err := m.MergeContexts("existing-name", "", []string{ctxA.ID, ctxB.ID}, MergeOptions{})
	if err == nil {
		t.Error("expected error when merged name already exists")
	}
}

func TestManagerMergeContexts_ContextNotFound(t *testing.T) {
	m := newTestManager(t)
	ctx := addTestContext(t, m, "real-ctx", sampleFiles(), ModeFull, nil)

	_, err := m.MergeContexts("new-merge", "", []string{ctx.ID, "fake-id"}, MergeOptions{})
	if err == nil {
		t.Error("expected error when a context ID does not exist")
	}
}

func TestManagerMergeContexts_SortByPath(t *testing.T) {
	m := newTestManager(t)
	ctxA := addTestContext(t, m, "sort-a", []utils.FileInfo{
		{Path: "z.go", Content: "z", Size: 1, Type: "Go"},
	}, ModeFull, nil)
	ctxB := addTestContext(t, m, "sort-b", []utils.FileInfo{
		{Path: "a.go", Content: "a", Size: 1, Type: "Go"},
	}, ModeFull, nil)

	merged, err := m.MergeContexts("sorted-merge", "", []string{ctxA.ID, ctxB.ID}, MergeOptions{
		SortByPath: true,
	})
	if err != nil {
		t.Fatalf("MergeContexts with sort failed: %v", err)
	}
	if merged.Files[0].Path != "a.go" {
		t.Errorf("expected first file to be a.go after sort, got %s", merged.Files[0].Path)
	}
}

// ---------------------------------------------------------------------------
// Manager tests: UpdateContext
// ---------------------------------------------------------------------------

func TestManagerUpdateContext_DescriptionAndTags(t *testing.T) {
	m := newTestManager(t)
	addTestContext(t, m, "update-me", sampleFiles(), ModeFull, []string{"old"})

	updated, err := m.UpdateContext("update-me", nil, "", []string{"new-tag"}, "new description")
	if err != nil {
		t.Fatalf("UpdateContext failed: %v", err)
	}
	if updated.Description != "new description" {
		t.Errorf("description not updated: got %q", updated.Description)
	}
	if len(updated.Tags) != 1 || updated.Tags[0] != "new-tag" {
		t.Errorf("tags not updated: got %v", updated.Tags)
	}
}

func TestManagerUpdateContext_NotFound(t *testing.T) {
	m := newTestManager(t)
	_, err := m.UpdateContext("nonexistent", nil, "", nil, "desc")
	if err == nil {
		t.Error("expected error updating nonexistent context")
	}
}

// ---------------------------------------------------------------------------
// Manager tests: BuildPromptMessages
// ---------------------------------------------------------------------------

func TestManagerBuildPromptMessages_NoAttachments(t *testing.T) {
	m := newTestManager(t)

	msgs, err := m.BuildPromptMessages("empty-session", FormatOptions{})
	if err != nil {
		t.Fatalf("BuildPromptMessages failed: %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil messages for session with no attachments, got %d", len(msgs))
	}
}

func TestManagerBuildPromptMessages_WithAttachment(t *testing.T) {
	m := newTestManager(t)
	ctx := addTestContext(t, m, "prompt-ctx", sampleFiles(), ModeFull, nil)

	if err := m.AttachContext("sess", ctx.ID, 0); err != nil {
		t.Fatal(err)
	}

	msgs, err := m.BuildPromptMessages("sess", FormatOptions{IncludeMetadata: true})
	if err != nil {
		t.Fatalf("BuildPromptMessages failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected role user, got %q", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "prompt-ctx") {
		t.Error("expected message content to contain context name")
	}
}

func TestManagerBuildPromptMessages_SystemRole(t *testing.T) {
	m := newTestManager(t)
	ctx := addTestContext(t, m, "system-ctx", sampleFiles(), ModeFull, nil)

	if err := m.AttachContext("sess", ctx.ID, 0); err != nil {
		t.Fatal(err)
	}

	msgs, err := m.BuildPromptMessages("sess", FormatOptions{Role: "system"})
	if err != nil {
		t.Fatalf("BuildPromptMessages failed: %v", err)
	}
	if msgs[0].Role != "system" {
		t.Errorf("expected role system, got %q", msgs[0].Role)
	}
}

func TestManagerBuildPromptMessages_InvalidRoleDefaultsToUser(t *testing.T) {
	m := newTestManager(t)
	ctx := addTestContext(t, m, "role-ctx", sampleFiles(), ModeFull, nil)

	if err := m.AttachContext("sess", ctx.ID, 0); err != nil {
		t.Fatal(err)
	}

	msgs, err := m.BuildPromptMessages("sess", FormatOptions{Role: "admin"})
	if err != nil {
		t.Fatalf("BuildPromptMessages failed: %v", err)
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected invalid role to default to user, got %q", msgs[0].Role)
	}
}

func TestManagerBuildPromptMessages_WithSelectedChunks(t *testing.T) {
	m := newTestManager(t)

	files := sampleFiles()
	ctx := addTestContext(t, m, "chunk-ctx", files, ModeChunked, nil)

	// Manually add chunks
	ctx.IsChunked = true
	ctx.Chunks = []FileChunk{
		{Index: 1, TotalChunks: 2, Files: files[:1], Description: "Chunk 1", TotalSize: 30, EstTokens: 10},
		{Index: 2, TotalChunks: 2, Files: files[1:], Description: "Chunk 2", TotalSize: 42, EstTokens: 15},
	}

	opts := AttachOptions{Priority: 0, SelectedChunks: []int{1}}
	if err := m.AttachContextWithOptions("sess", ctx.ID, opts); err != nil {
		t.Fatal(err)
	}

	msgs, err := m.BuildPromptMessages("sess", FormatOptions{IncludeMetadata: true})
	if err != nil {
		t.Fatalf("BuildPromptMessages failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	// Should mention chunks
	if !strings.Contains(msgs[0].Content, "Chunks") {
		t.Error("expected message to reference chunks")
	}
}

// ---------------------------------------------------------------------------
// Manager tests: GetMetrics
// ---------------------------------------------------------------------------

func TestManagerGetMetrics(t *testing.T) {
	m := newTestManager(t)
	addTestContext(t, m, "metrics-a", sampleFiles(), ModeFull, nil)
	addTestContext(t, m, "metrics-b", sampleFiles(), ModeSummary, nil)

	ctx := addTestContext(t, m, "metrics-c", sampleFiles(), ModeFull, nil)
	_ = m.AttachContext("sess", ctx.ID, 0)

	metrics := m.GetMetrics()

	if metrics.TotalContexts != 3 {
		t.Errorf("expected 3 total contexts, got %d", metrics.TotalContexts)
	}
	if metrics.AttachedContexts != 1 {
		t.Errorf("expected 1 attached context, got %d", metrics.AttachedContexts)
	}
	if metrics.TotalFiles != 9 { // 3 files * 3 contexts
		t.Errorf("expected 9 total files, got %d", metrics.TotalFiles)
	}
	if metrics.ContextsByMode[string(ModeFull)] != 2 {
		t.Errorf("expected 2 full mode contexts, got %d", metrics.ContextsByMode[string(ModeFull)])
	}
	if metrics.ContextsByMode[string(ModeSummary)] != 1 {
		t.Errorf("expected 1 summary mode context, got %d", metrics.ContextsByMode[string(ModeSummary)])
	}
}

// ---------------------------------------------------------------------------
// Processor tests
// ---------------------------------------------------------------------------

func TestProcessorEstimateTokenCount(t *testing.T) {
	p := NewProcessor(zap.NewNop())

	files := []utils.FileInfo{
		{Content: strings.Repeat("a", 400)}, // ~100 tokens
		{Content: strings.Repeat("b", 200)}, // ~50 tokens
	}

	tokens := p.EstimateTokenCount(files)
	if tokens != 150 {
		t.Errorf("expected 150 estimated tokens, got %d", tokens)
	}
}

func TestProcessorProcessPaths_NoPaths(t *testing.T) {
	p := NewProcessor(zap.NewNop())
	_, _, err := p.ProcessPaths(nil, ModeFull)
	if err == nil {
		t.Error("expected error when no paths provided")
	}
}

func TestProcessorProcessPaths_InvalidMode(t *testing.T) {
	p := NewProcessor(zap.NewNop())
	_, _, err := p.ProcessPaths([]string{"."}, "invalid-mode")
	if err == nil {
		t.Error("expected error for invalid processing mode")
	}
}

// ---------------------------------------------------------------------------
// Chunker tests
// ---------------------------------------------------------------------------

func TestChunkerDivideIntoChunks_Empty(t *testing.T) {
	c := NewChunker(zap.NewNop())
	chunks, err := c.DivideIntoChunks(nil, ChunkSmart)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chunks != nil {
		t.Errorf("expected nil for empty files, got %d chunks", len(chunks))
	}
}

func TestChunkerDivideIntoChunks_FewFiles(t *testing.T) {
	c := NewChunker(zap.NewNop())

	files := make([]utils.FileInfo, MinFilesForChunking-1)
	for i := range files {
		files[i] = utils.FileInfo{Path: "f.go", Content: "x", Size: 1, Type: "Go"}
	}

	chunks, err := c.DivideIntoChunks(files, ChunkSmart)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk for few files, got %d", len(chunks))
	}
	if chunks[0].Index != 1 || chunks[0].TotalChunks != 1 {
		t.Error("expected single chunk with index 1 and total 1")
	}
}

func TestChunkerDivideIntoChunks_BySize(t *testing.T) {
	c := NewChunker(zap.NewNop())

	// Create enough files to trigger chunking
	files := make([]utils.FileInfo, 20)
	for i := range files {
		// Each file ~30000 tokens worth of content (120KB)
		content := strings.Repeat("x", 120*1024)
		files[i] = utils.FileInfo{
			Path:    filepath.Join("dir", "file"+string(rune('a'+i))+".go"),
			Content: content,
			Size:    int64(len(content)),
			Type:    "Go",
		}
	}

	chunks, err := c.DivideIntoChunks(files, ChunkBySize)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 2 {
		t.Errorf("expected multiple chunks for large dataset, got %d", len(chunks))
	}

	// Verify indices are correct
	for i, chunk := range chunks {
		if chunk.Index != i+1 {
			t.Errorf("chunk %d has index %d", i, chunk.Index)
		}
		if chunk.TotalChunks != len(chunks) {
			t.Errorf("chunk %d has TotalChunks %d, expected %d", i, chunk.TotalChunks, len(chunks))
		}
	}
}

func TestChunkerDivideIntoChunks_ByDirectory(t *testing.T) {
	c := NewChunker(zap.NewNop())

	files := make([]utils.FileInfo, 0)
	// Create files across many directories
	for i := 0; i < 15; i++ {
		dir := filepath.Join("project", "module"+string(rune('a'+i)))
		files = append(files, utils.FileInfo{
			Path:    filepath.Join(dir, "main.go"),
			Content: strings.Repeat("x", 1000),
			Size:    1000,
			Type:    "Go",
		})
	}

	chunks, err := c.DivideIntoChunks(files, ChunkByDirectory)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Error("expected at least 1 chunk")
	}
}

func TestChunkerDivideIntoChunks_ByFileType(t *testing.T) {
	c := NewChunker(zap.NewNop())

	files := make([]utils.FileInfo, 0)
	types := []string{"Go", "Python", "JavaScript", "TypeScript", "Rust"}
	for i := 0; i < 15; i++ {
		ft := types[i%len(types)]
		files = append(files, utils.FileInfo{
			Path:    filepath.Join("src", "file"+string(rune('a'+i))+"."+ft),
			Content: strings.Repeat("x", 500),
			Size:    500,
			Type:    ft,
		})
	}

	chunks, err := c.DivideIntoChunks(files, ChunkByFileType)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Error("expected at least 1 chunk")
	}
}

// ---------------------------------------------------------------------------
// Processing modes
// ---------------------------------------------------------------------------

func TestProcessingModeConstants(t *testing.T) {
	if ModeFull != "full" {
		t.Errorf("ModeFull = %q, want 'full'", ModeFull)
	}
	if ModeSummary != "summary" {
		t.Errorf("ModeSummary = %q, want 'summary'", ModeSummary)
	}
	if ModeChunked != "chunked" {
		t.Errorf("ModeChunked = %q, want 'chunked'", ModeChunked)
	}
	if ModeSmart != "smart" {
		t.Errorf("ModeSmart = %q, want 'smart'", ModeSmart)
	}
}

func TestChunkStrategyConstants(t *testing.T) {
	if ChunkByDirectory != "directory" {
		t.Errorf("ChunkByDirectory = %q, want 'directory'", ChunkByDirectory)
	}
	if ChunkByFileType != "filetype" {
		t.Errorf("ChunkByFileType = %q, want 'filetype'", ChunkByFileType)
	}
	if ChunkBySize != "size" {
		t.Errorf("ChunkBySize = %q, want 'size'", ChunkBySize)
	}
	if ChunkSmart != "smart" {
		t.Errorf("ChunkSmart = %q, want 'smart'", ChunkSmart)
	}
}

// ---------------------------------------------------------------------------
// Processor: createSummaryView
// ---------------------------------------------------------------------------

func TestProcessorCreateSummaryView(t *testing.T) {
	p := NewProcessor(zap.NewNop())

	longContent := strings.Repeat("line\n", 100) // 100 lines
	files := []utils.FileInfo{
		{Path: "long.go", Content: longContent, Size: int64(len(longContent)), Type: "Go"},
		{Path: "short.go", Content: "short", Size: 5, Type: "Go"},
	}

	summary := p.createSummaryView(files)

	if len(summary) != 2 {
		t.Fatalf("expected 2 summary files, got %d", len(summary))
	}

	// Long file should be truncated
	if !strings.Contains(summary[0].Content, "omitidas") {
		t.Error("expected long file to be truncated with omission notice")
	}

	// Short file should remain unchanged
	if summary[1].Content != "short" {
		t.Errorf("expected short file to be unmodified, got %q", summary[1].Content)
	}
}
