package workspace

import (
	"path/filepath"
	"time"

	"github.com/diillson/chatcli/cli/workspace/memory"
	"go.uber.org/zap"
)

// DailyNote represents a single day's note.
type DailyNote struct {
	Date    time.Time
	Path    string
	Content string
}

// MemoryStore is the backward-compatible facade that delegates to the
// new structured memory.Manager. All existing callers (ContextBuilder,
// memoryWorker, memory_command) continue to work through this interface.
type MemoryStore struct {
	manager   *memory.Manager
	memoryDir string
	logger    *zap.Logger
}

// NewMemoryStore creates a new memory store backed by the structured Manager.
func NewMemoryStore(baseDir string, logger *zap.Logger) *MemoryStore {
	memDir := filepath.Join(baseDir, "memory")
	mgr := memory.NewManager(memDir, memory.DefaultConfig(), logger)

	return &MemoryStore{
		manager:   mgr,
		memoryDir: memDir,
		logger:    logger,
	}
}

// Manager returns the underlying memory.Manager for advanced operations.
func (ms *MemoryStore) Manager() *memory.Manager {
	return ms.manager
}

// EnsureDirectories creates the memory directory structure.
func (ms *MemoryStore) EnsureDirectories() error {
	return ms.manager.EnsureDirectories()
}

// --- Long-term Memory ---

// ReadLongTerm returns the rendered long-term memory content.
func (ms *MemoryStore) ReadLongTerm() string {
	return ms.manager.ReadLongTerm()
}

// WriteLongTerm replaces all long-term memory.
func (ms *MemoryStore) WriteLongTerm(content string) error {
	return ms.manager.WriteLongTerm(content)
}

// AppendLongTerm adds new content to long-term memory.
func (ms *MemoryStore) AppendLongTerm(entry string) error {
	return ms.manager.AppendLongTerm(entry)
}

// --- Daily Notes ---

// TodayNotePath returns the path for today's note.
func (ms *MemoryStore) TodayNotePath() string {
	return ms.manager.TodayNotePath()
}

// WriteDailyNote appends to today's daily note.
func (ms *MemoryStore) WriteDailyNote(entry string) error {
	return ms.manager.WriteDailyNote(entry)
}

// GetRecentDailyNotes returns the last N days of notes.
func (ms *MemoryStore) GetRecentDailyNotes(days int) []DailyNote {
	managerNotes := ms.manager.GetRecentDailyNotes(days)
	notes := make([]DailyNote, len(managerNotes))
	for i, n := range managerNotes {
		notes[i] = DailyNote{
			Date:    n.Date,
			Path:    n.Path,
			Content: n.Content,
		}
	}
	return notes
}

// GetMemoryContext builds the memory section for the system prompt.
// Uses smart retrieval when no hints are provided.
func (ms *MemoryStore) GetMemoryContext() string {
	return ms.manager.GetMemoryContext()
}

// GetRelevantContext returns memory tailored to conversation hints.
func (ms *MemoryStore) GetRelevantContext(hints []string) string {
	return ms.manager.GetRelevantContext(hints)
}

// ProcessExtraction processes enhanced extraction output from the memory worker.
func (ms *MemoryStore) ProcessExtraction(response string) {
	ms.manager.ProcessExtraction(response)
}

// RecordInteraction records a usage event.
func (ms *MemoryStore) RecordInteraction(event memory.InteractionEvent) {
	ms.manager.RecordInteraction(event)
}
