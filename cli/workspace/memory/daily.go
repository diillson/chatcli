package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

// DailyNoteStore manages daily note files.
type DailyNoteStore struct {
	memoryDir string
	logger    *zap.Logger
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating daily notes dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening daily note: %w", err)
	}
	defer f.Close()

	ts := time.Now().Format("15:04")
	_, err = f.WriteString(fmt.Sprintf("\n## %s\n\n%s\n", ts, entry))
	return err
}

// GetRecentDailyNotes returns the last N days of notes.
func (d *DailyNoteStore) GetRecentDailyNotes(days int) []DailyNote {
	var notes []DailyNote
	now := time.Now()

	for i := 0; i < days; i++ {
		date := now.AddDate(0, 0, -i)
		path := filepath.Join(d.memoryDir, date.Format("200601"), date.Format("20060102")+".md")
		data, err := os.ReadFile(path)
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
			if rmErr := os.Remove(path); rmErr != nil {
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
