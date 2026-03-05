package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

// MemoryStore manages persistent AI memory.
type MemoryStore struct {
	memoryDir string // ~/.chatcli/memory/
	logger    *zap.Logger
}

// DailyNote represents a single day's note.
type DailyNote struct {
	Date    time.Time
	Path    string
	Content string
}

// NewMemoryStore creates a new memory store.
func NewMemoryStore(baseDir string, logger *zap.Logger) *MemoryStore {
	return &MemoryStore{
		memoryDir: filepath.Join(baseDir, "memory"),
		logger:    logger,
	}
}

// EnsureDirectories creates the memory directory structure.
func (ms *MemoryStore) EnsureDirectories() error {
	return os.MkdirAll(ms.memoryDir, 0o755)
}

// --- Long-term Memory ---

func (ms *MemoryStore) memoryFilePath() string {
	return filepath.Join(ms.memoryDir, "MEMORY.md")
}

// ReadLongTerm reads the main MEMORY.md file.
func (ms *MemoryStore) ReadLongTerm() string {
	data, err := os.ReadFile(ms.memoryFilePath())
	if err != nil {
		if !os.IsNotExist(err) {
			ms.logger.Debug("failed to read long-term memory", zap.Error(err))
		}
		return ""
	}
	return string(data)
}

// WriteLongTerm writes the entire MEMORY.md file.
func (ms *MemoryStore) WriteLongTerm(content string) error {
	if err := ms.EnsureDirectories(); err != nil {
		return err
	}
	return os.WriteFile(ms.memoryFilePath(), []byte(content), 0o644)
}

// AppendLongTerm appends to MEMORY.md.
func (ms *MemoryStore) AppendLongTerm(entry string) error {
	if err := ms.EnsureDirectories(); err != nil {
		return err
	}
	f, err := os.OpenFile(ms.memoryFilePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening memory file: %w", err)
	}
	defer f.Close()
	_, err = f.WriteString("\n" + entry)
	return err
}

// --- Daily Notes ---

// TodayNotePath returns the path for today's note.
func (ms *MemoryStore) TodayNotePath() string {
	now := time.Now()
	return filepath.Join(ms.memoryDir, now.Format("200601"), now.Format("20060102")+".md")
}

// WriteDailyNote appends to today's daily note.
func (ms *MemoryStore) WriteDailyNote(entry string) error {
	path := ms.TodayNotePath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating daily notes dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening daily note: %w", err)
	}
	defer f.Close()

	// Add timestamp header
	ts := time.Now().Format("15:04")
	_, err = f.WriteString(fmt.Sprintf("\n## %s\n\n%s\n", ts, entry))
	return err
}

// GetRecentDailyNotes returns the last N days of notes.
func (ms *MemoryStore) GetRecentDailyNotes(days int) []DailyNote {
	var notes []DailyNote
	now := time.Now()

	for i := 0; i < days; i++ {
		date := now.AddDate(0, 0, -i)
		path := filepath.Join(ms.memoryDir, date.Format("200601"), date.Format("20060102")+".md")
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

	// Sort oldest first
	sort.Slice(notes, func(i, j int) bool {
		return notes[i].Date.Before(notes[j].Date)
	})

	return notes
}

// GetMemoryContext builds the memory section for the system prompt.
func (ms *MemoryStore) GetMemoryContext() string {
	var parts []string

	longTerm := ms.ReadLongTerm()
	if strings.TrimSpace(longTerm) != "" {
		parts = append(parts, "## Long-term Memory\n\n"+longTerm)
	}

	recentNotes := ms.GetRecentDailyNotes(3)
	if len(recentNotes) > 0 {
		var notesParts []string
		for _, note := range recentNotes {
			dateStr := note.Date.Format("2006-01-02")
			notesParts = append(notesParts, fmt.Sprintf("### %s\n\n%s", dateStr, note.Content))
		}
		parts = append(parts, "## Recent Daily Notes\n\n"+strings.Join(notesParts, "\n\n"))
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}
