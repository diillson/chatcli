/*
 * ChatCLI - Scheduler: append-only JSONL audit log.
 *
 * Every scheduler mutation (create, transition, cancel, fire) writes a
 * line to <dir>/audit.log. Operators and compliance auditors consume
 * this file via their existing log pipelines. The file is a plain
 * JSONL (one JSON object per line) to keep parsers simple.
 *
 * Rotation: lumberjack handles size-based rolling. Default 10 MiB per
 * file, 7 backups kept. Configurable via Config.AuditMaxSizeMB.
 */
package scheduler

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"

	"go.uber.org/zap"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

// auditLogger owns the rotating file handle and a single write mutex.
type auditLogger struct {
	mu      sync.Mutex
	writer  *lumberjack.Logger
	logger  *zap.Logger
	path    string
	metrics *Metrics
}

// newAuditLogger constructs the logger. dir is the scheduler data
// directory; the audit file is <dir>/audit.log.
func newAuditLogger(dir string, maxSizeMB, maxBackups, maxAgeDays int, logger *zap.Logger, m *Metrics) *auditLogger {
	if maxSizeMB <= 0 {
		maxSizeMB = 10
	}
	if maxBackups <= 0 {
		maxBackups = 7
	}
	path := filepath.Join(dir, "audit.log")
	return &auditLogger{
		writer: &lumberjack.Logger{
			Filename:   path,
			MaxSize:    maxSizeMB,
			MaxBackups: maxBackups,
			MaxAge:     maxAgeDays,
			Compress:   true,
			LocalTime:  true,
		},
		logger:  logger,
		path:    path,
		metrics: m,
	}
}

// Write appends one event as a JSON line.
func (a *auditLogger) Write(evt Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	line, err := json.Marshal(evt)
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("scheduler audit: marshal failed", zap.Error(err))
		}
		return
	}
	if _, err := a.writer.Write(line); err != nil {
		if a.logger != nil {
			a.logger.Warn("scheduler audit: write failed", zap.Error(err))
		}
		return
	}
	if _, err := a.writer.Write([]byte("\n")); err != nil {
		if a.logger != nil {
			a.logger.Warn("scheduler audit: newline write failed", zap.Error(err))
		}
		return
	}
	if a.metrics != nil {
		a.metrics.AuditWrites.Inc()
	}
}

// Close releases the underlying file handle.
func (a *auditLogger) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.writer != nil {
		return a.writer.Close()
	}
	return nil
}

// Path returns the audit file path — used by /config scheduler to
// show the operator where to find it.
func (a *auditLogger) Path() string { return a.path }

// nopAuditLogger is used when audit is disabled; avoids nil-check churn.
type nopAuditLogger struct{}

func (nopAuditLogger) Write(_ Event) {}
func (nopAuditLogger) Close() error  { return nil }
func (nopAuditLogger) Path() string  { return "" }

// auditWriter abstracts the two implementations so Scheduler can hold
// one interface-typed field.
type auditWriter interface {
	Write(Event)
	Close() error
	Path() string
}

// NewAuditFileWriter returns a file-backed audit writer. Exposed for
// the daemon, which wants the path reported via IPC.
func NewAuditFileWriter(dir string, maxSizeMB, maxBackups, maxAgeDays int, logger *zap.Logger, m *Metrics) auditWriter {
	al := newAuditLogger(dir, maxSizeMB, maxBackups, maxAgeDays, logger, m)
	return al
}

// NopAuditWriter returns the no-op audit writer.
func NopAuditWriter() auditWriter { return nopAuditLogger{} }

// ensureErr keeps the error variable declared so linters don't flag it.
// Used internally when building formatted error messages.
var _ = fmt.Errorf
