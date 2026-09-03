/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * LLM request audit trail. The gRPC server already audited transport
 * metadata; nothing recorded what the interactive surfaces sent to which
 * provider. With CHATCLI_AUDIT_LOG_PATH set, every request on every
 * surface now leaves a JSON line: when, which provider and model, how
 * large the payload was, how much history and how many cache markers it
 * carried, the outcome, the latency, the token usage the provider
 * reported and the running count of secrets redacted from what the model
 * received. Never the prompt content. The sink hangs off the observability
 * chokepoint every adapter passes through, so a new provider is audited
 * the day it ships.
 */
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/llm/client"
	"go.uber.org/zap"
)

// AuditLogPathEnv is shared with the gRPC audit logger: one file, one
// format, both surfaces.
const AuditLogPathEnv = "CHATCLI_AUDIT_LOG_PATH"

// redactionsTotal counts content rewrites the LLM-path redactor performed
// in this process; reported on every audit line so an operator can tell a
// session that carried secrets from one that did not.
var redactionsTotal atomic.Int64

// llmAuditEntry is one audit line.
type llmAuditEntry struct {
	Timestamp  string            `json:"timestamp"`
	Kind       string            `json:"kind"` // "llm"
	Phase      string            `json:"phase"`
	Surface    string            `json:"surface,omitempty"`
	Session    string            `json:"session,omitempty"`
	Provider   string            `json:"provider"`
	Model      string            `json:"model"`
	Status     string            `json:"status,omitempty"`
	DurationMS int64             `json:"duration_ms,omitempty"`
	Redactions int64             `json:"redactions_total"`
	Fields     map[string]string `json:"fields,omitempty"`
}

// llmAuditWriter appends entries to the audit file.
type llmAuditWriter struct {
	mu      sync.Mutex
	f       *os.File
	enc     *json.Encoder
	surface string
	session func() string
	logger  *zap.Logger
}

// initLLMAudit installs the request auditor when the audit path is
// configured. surface names the process role for the trail (repl, oneshot,
// gateway, mcp, ...). Failures are logged and leave auditing off — the
// trail must never block a session from starting.
func (cli *ChatCLI) initLLMAudit(surface string) {
	path := os.Getenv(AuditLogPathEnv)
	if path == "" {
		return
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		if cli.logger != nil {
			cli.logger.Error("audit log path must be absolute; LLM audit disabled", zap.String("path", path))
		}
		return
	}
	f, err := os.OpenFile(clean, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 G703 -- operator-configured absolute path (env), cleaned and required absolute above
	if err != nil {
		if cli.logger != nil {
			cli.logger.Error("failed to open audit log for LLM requests", zap.String("path", clean), zap.Error(err))
		}
		return
	}
	w := &llmAuditWriter{f: f, enc: json.NewEncoder(f), surface: surface, logger: cli.logger}
	w.session = func() string {
		if cli.costTracker == nil {
			return ""
		}
		return cli.costTracker.sessionIDSnapshot()
	}
	cli.llmAudit = w
	client.RegisterRequestAuditor(w.record)
}

// record is the RequestAuditor callback.
func (w *llmAuditWriter) record(ev client.RequestAuditEvent) {
	entry := llmAuditEntry{
		Timestamp:  ev.Time.UTC().Format(time.RFC3339Nano),
		Kind:       "llm",
		Phase:      ev.Phase,
		Surface:    w.surface,
		Provider:   ev.Provider,
		Model:      ev.Model,
		Status:     ev.Status,
		Redactions: redactionsTotal.Load(),
		Fields:     ev.Fields,
	}
	if ev.Duration > 0 {
		entry.DurationMS = ev.Duration.Milliseconds()
	}
	if w.session != nil {
		entry.Session = w.session()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.enc.Encode(entry); err != nil && w.logger != nil {
		w.logger.Warn("audit log write failed", zap.Error(err))
	}
}

// close detaches the sink and closes the file.
func (w *llmAuditWriter) close() {
	if w == nil {
		return
	}
	client.RegisterRequestAuditor(nil)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f != nil {
		_ = w.f.Sync()
		_ = w.f.Close()
		w.f = nil
	}
}
