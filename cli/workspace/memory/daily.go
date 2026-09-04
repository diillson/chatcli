package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// DailyNoteStore manages daily note files.
type DailyNoteStore struct {
	memoryDir string
	logger    *zap.Logger
	writeMu   sync.Mutex
}

// NewDailyNoteStore creates a new daily note store.
func NewDailyNoteStore(memoryDir string, logger *zap.Logger) *DailyNoteStore {
	return &DailyNoteStore{memoryDir: memoryDir, logger: logger}
}

// TodayNotePath returns the path for today's note.
func (d *DailyNoteStore) TodayNotePath() string {
	now := time.Now()
	return filepath.Join(d.memoryDir, now.Format("200601"), now.Format("20060102")+".md")
}

// WriteDailyNote appends to today's daily note.
func (d *DailyNoteStore) WriteDailyNote(entry string) error {
	path := d.TodayNotePath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating daily notes dir: %w", err)
	}

	// Read-modify-write through an atomic rename: a crash mid-append can
	// never leave a torn note, and readers only ever see a whole file.
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	existing, err := os.ReadFile(path) //#nosec G304 -- path under the memory dir
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading daily note: %w", err)
	}
	ts := time.Now().Format("15:04")
	next := append(existing, []byte(fmt.Sprintf("\n## %s\n\n%s\n", ts, entry))...)
	// Daily notes are human-editable Markdown by contract (never sealed):
	// the plain atomic writer, not the sealing one.
	if err := utils.AtomicWriteFile(path, next, 0o600); err != nil {
		return fmt.Errorf("writing daily note: %w", err)
	}
	return nil
}

// GetRecentDailyNotes returns the last N days of notes.
func (d *DailyNoteStore) GetRecentDailyNotes(days int) []DailyNote {
	var notes []DailyNote
	now := time.Now()

	for i := 0; i < days; i++ {
		date := now.AddDate(0, 0, -i)
		path := filepath.Join(d.memoryDir, date.Format("200601"), date.Format("20060102")+".md")
		data, err := os.ReadFile(path) //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		notes = append(notes, DailyNote{
			Date:    date,
			Path:    path,
			Content: content,
		})
	}

	sort.Slice(notes, func(i, j int) bool {
		return notes[i].Date.Before(notes[j].Date)
	})

	return notes
}

// Cleanup deletes daily notes older than retentionDays.
// Returns the number of files deleted.
func (d *DailyNoteStore) Cleanup(retentionDays int) (int, error) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	deleted := 0

	err := filepath.Walk(d.memoryDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".md") || info.Name() == "MEMORY.md" {
			return nil
		}

		// Parse date from filename (YYYYMMDD.md)
		name := strings.TrimSuffix(info.Name(), ".md")
		noteDate, parseErr := time.Parse("20060102", name)
		if parseErr != nil {
			return nil // not a date-formatted file, skip
		}

		if noteDate.Before(cutoff) {
			if rmErr := os.Remove(filepath.Clean(path)); rmErr != nil { //#nosec G122 -- path from filepath.Walk under memory dir, name validated above
				d.logger.Warn("failed to delete old daily note",
					zap.String("path", path), zap.Error(rmErr))
			} else {
				deleted++
				d.logger.Debug("deleted old daily note", zap.String("path", path))
			}
		}
		return nil
	})

	// Cleanup empty month directories
	d.cleanEmptyDirs()

	return deleted, err
}

// cleanEmptyDirs removes empty YYYYMM directories.
func (d *DailyNoteStore) cleanEmptyDirs() {
	entries, err := os.ReadDir(d.memoryDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Only clean YYYYMM-formatted directories
		if len(entry.Name()) != 6 {
			continue
		}
		if _, err := time.Parse("200601", entry.Name()); err != nil {
			continue
		}
		dirPath := filepath.Join(d.memoryDir, entry.Name())
		subEntries, err := os.ReadDir(dirPath)
		if err != nil || len(subEntries) > 0 {
			continue
		}
		_ = os.Remove(dirPath)
	}
}
