/*
 * ChatCLI - File Staleness Tracker
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Tracks file state (mtime + content hash) between read and write operations
 * to detect when a file has been modified externally. This prevents the LLM
 * from silently overwriting changes made by the user or other processes.
 *
 * Inspired by openclaude's file staleness detection with mtime + hash fallback.
 *
 * Usage:
 *   tracker.RecordRead("/path/to/file")         // after successful read
 *   stale, diff := tracker.CheckStaleness(path) // before write/patch
 */
package agent

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileState records the state of a file at the time it was read.
type FileState struct {
	Path        string    `json:"path"`
	ModTime     time.Time `json:"mod_time"`
	ContentHash string    `json:"content_hash"` // SHA-256 hex
	Size        int64     `json:"size"`
	ReadAt      time.Time `json:"read_at"`
}

// StalenessResult describes whether a file has changed since it was last read.
type StalenessResult struct {
	IsStale      bool
	Reason       string // human-readable reason
	OriginalHash string
	CurrentHash  string
	OriginalMod  time.Time
	CurrentMod   time.Time
	OriginalSize int64
	CurrentSize  int64
}

// FileStalenessTracker tracks file states across read/write operations.
type FileStalenessTracker struct {
	mu    sync.RWMutex
	files map[string]*FileState // path → state at last read
}

// NewFileStalenessTracker creates a new tracker.
func NewFileStalenessTracker() *FileStalenessTracker {
	return &FileStalenessTracker{
		files: make(map[string]*FileState),
	}
}

// RecordRead records the current state of a file after a successful read.
// Should be called after every read tool execution.
func (t *FileStalenessTracker) RecordRead(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat file for staleness tracking: %w", err)
	}

	content, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("read file for staleness tracking: %w", err)
	}

	hash := sha256.Sum256(content)

	state := &FileState{
		Path:        path,
		ModTime:     info.ModTime(),
		ContentHash: fmt.Sprintf("%x", hash),
		Size:        info.Size(),
		ReadAt:      time.Now(),
	}

	t.mu.Lock()
	t.files[path] = state
	t.mu.Unlock()

	return nil
}

// CheckStaleness checks if a file has been modified since the last recorded read.
// Returns a StalenessResult. If the file was never read, it's not considered stale.
func (t *FileStalenessTracker) CheckStaleness(path string) StalenessResult {
	t.mu.RLock()
	recorded, exists := t.files[path]
	t.mu.RUnlock()

	if !exists {
		return StalenessResult{IsStale: false, Reason: "file not previously tracked"}
	}

	// Step 1: Quick check via mtime
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return StalenessResult{
				IsStale: true,
				Reason:  "file was deleted since last read",
			}
		}
		// Can't stat — assume not stale to avoid blocking
		return StalenessResult{IsStale: false, Reason: "stat failed, assuming not stale"}
	}

	result := StalenessResult{
		OriginalHash: recorded.ContentHash,
		OriginalMod:  recorded.ModTime,
		OriginalSize: recorded.Size,
		CurrentMod:   info.ModTime(),
		CurrentSize:  info.Size(),
	}

	// If mtime and size are unchanged, very likely not stale
	if info.ModTime().Equal(recorded.ModTime) && info.Size() == recorded.Size {
		result.IsStale = false
		result.Reason = "mtime and size unchanged"
		return result
	}

	// Step 2: Mtime or size changed — do full content hash comparison
	content, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		result.IsStale = true
		result.Reason = "mtime changed and file unreadable"
		return result
	}

	hash := sha256.Sum256(content)
	currentHash := fmt.Sprintf("%x", hash)
	result.CurrentHash = currentHash

	if currentHash == recorded.ContentHash {
		// Content is identical despite mtime change (e.g., touch without edit)
		result.IsStale = false
		result.Reason = "mtime changed but content identical"
		return result
	}

	result.IsStale = true
	result.Reason = fmt.Sprintf("file modified externally (mtime: %s → %s, size: %d → %d)",
		recorded.ModTime.Format(time.RFC3339),
		info.ModTime().Format(time.RFC3339),
		recorded.Size, info.Size())
	return result
}

// FormatWarning generates a warning message for stale files, suitable for
// injecting into tool results so the LLM can decide how to proceed.
func (r *StalenessResult) FormatWarning(path string) string {
	if !r.IsStale {
		return ""
	}
	return fmt.Sprintf(
		"WARNING: File %q has been modified externally since you last read it.\n"+
			"Reason: %s\n"+
			"You should re-read the file before making changes to avoid overwriting external edits.\n"+
			"If you proceed anyway, external changes will be lost.",
		path, r.Reason)
}

// Clear removes the recorded state for a file (e.g., after a successful write).
func (t *FileStalenessTracker) Clear(path string) {
	t.mu.Lock()
	delete(t.files, path)
	t.mu.Unlock()
}

// ClearAll removes all tracked file states.
func (t *FileStalenessTracker) ClearAll() {
	t.mu.Lock()
	t.files = make(map[string]*FileState)
	t.mu.Unlock()
}

// TrackedFiles returns the list of currently tracked file paths.
func (t *FileStalenessTracker) TrackedFiles() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	paths := make([]string, 0, len(t.files))
	for p := range t.files {
		paths = append(paths, p)
	}
	return paths
}
