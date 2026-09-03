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
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
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

	// Tenant is the gateway principal whose store set served the request
	// ("" for the shared set); the token counts are the provider's own
	// usage for the answered request (recv lines only).
	Tenant           string `json:"tenant,omitempty"`
	InputTokens      int    `json:"input_tokens,omitempty"`
	OutputTokens     int    `json:"output_tokens,omitempty"`
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int    `json:"cache_write_tokens,omitempty"`

	// Seq, PrevHash and Hash chain the trail: Hash = SHA-256(PrevHash ‖
	// canonical JSON of the entry without Hash). A removed, edited or
	// reordered line breaks the chain from that point on (VerifyAuditChain).
	Seq      int64  `json:"seq"`
	PrevHash string `json:"prev_hash,omitempty"`
	Hash     string `json:"hash"`
}

// llmAuditWriter appends entries to the audit file.
type llmAuditWriter struct {
	mu       sync.Mutex
	f        *os.File
	enc      *json.Encoder
	seq      int64
	lastHash string
	surface  string
	session  func() string
	tenant   func() string
	usage    func() *models.UsageInfo
	logger   *zap.Logger
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
	w.seq, w.lastHash = auditChainTail(clean)
	w.session = func() string {
		if cli.costTracker == nil {
			return ""
		}
		return cli.costTracker.sessionIDSnapshot()
	}
	w.tenant = cli.activeTenant
	w.usage = func() *models.UsageInfo {
		if ua, ok := cli.Client.(client.UsageAwareClient); ok && cli.Client != nil {
			return ua.LastUsage()
		}
		return nil
	}
	cli.llmAudit = w
	client.RegisterRequestAuditor(w.record)
}

// setSurface renames the surface for entries from now on (the gateway,
// ACP, tool and watch entrypoints share NewChatCLI with the REPL).
func (w *llmAuditWriter) setSurface(surface string) {
	if w == nil || surface == "" {
		return
	}
	w.mu.Lock()
	w.surface = surface
	w.mu.Unlock()
}

// SetAuditSurface names the process role on every audit line from now on.
func (cli *ChatCLI) SetAuditSurface(surface string) {
	if cli == nil || cli.llmAudit == nil {
		return
	}
	cli.llmAudit.setSurface(surface)
}

// record is the RequestAuditor callback.
func (w *llmAuditWriter) record(ev client.RequestAuditEvent) {
	w.mu.Lock()
	surface := w.surface
	w.mu.Unlock()
	entry := llmAuditEntry{
		Timestamp:  ev.Time.UTC().Format(time.RFC3339Nano),
		Kind:       "llm",
		Phase:      ev.Phase,
		Surface:    surface,
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
	if w.tenant != nil {
		entry.Tenant = w.tenant()
	}
	if ev.Phase == "recv" && ev.Status == "success" && w.usage != nil {
		if u := w.usage(); u != nil {
			entry.InputTokens = u.PromptTokens
			entry.OutputTokens = u.CompletionTokens
			entry.CacheReadTokens = u.CacheReadInputTokens
			entry.CacheWriteTokens = u.CacheCreationInputTokens
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	entry.Seq = w.seq
	entry.PrevHash = w.lastHash
	entry.Hash = auditEntryHash(entry)
	if err := w.enc.Encode(entry); err != nil && w.logger != nil {
		w.logger.Warn("audit log write failed", zap.Error(err))
		return
	}
	w.lastHash = entry.Hash
}

// auditEntryHash computes the chain hash of an entry (its Hash field
// excluded, PrevHash included).
func auditEntryHash(e llmAuditEntry) string {
	e.Hash = ""
	canonical, err := json.Marshal(e)
	if err != nil {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(e.PrevHash))
	h.Write([]byte{0})
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil))
}

// auditChainTail returns the last sequence number and hash recorded in an
// existing trail (0, "" for a new or legacy file), so a restarted process
// continues the chain instead of starting a new one.
func auditChainTail(path string) (int64, string) {
	f, err := os.Open(path) // #nosec G304 -- operator-configured audit path, cleaned by the caller
	if err != nil {
		return 0, ""
	}
	defer func() { _ = f.Close() }()
	var seq int64
	var last string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	for sc.Scan() {
		var e llmAuditEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil || e.Hash == "" {
			continue
		}
		seq, last = e.Seq, e.Hash
	}
	return seq, last
}

// AuditChainReport is the outcome of VerifyAuditChain.
type AuditChainReport struct {
	Entries  int
	Chained  int // entries carrying a hash
	Legacy   int // entries written before the chain existed
	BrokenAt int // 1-based line of the first break (0 = intact)
	Err      string
}

// Intact reports whether every chained entry verified.
func (r AuditChainReport) Intact() bool { return r.BrokenAt == 0 }

// VerifyAuditChain re-hashes a trail and reports the first line whose hash
// or previous-hash link does not match.
func VerifyAuditChain(path string) (AuditChainReport, error) {
	var rep AuditChainReport
	f, err := os.Open(path) // #nosec G304 -- operator-supplied audit path
	if err != nil {
		return rep, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	prev := ""
	line := 0
	for sc.Scan() {
		line++
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		rep.Entries++
		var e llmAuditEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			rep.BrokenAt, rep.Err = line, "unparseable entry"
			return rep, nil
		}
		if e.Hash == "" {
			rep.Legacy++
			continue
		}
		rep.Chained++
		if e.PrevHash != prev {
			rep.BrokenAt, rep.Err = line, "previous-hash link mismatch"
			return rep, nil
		}
		if auditEntryHash(e) != e.Hash {
			rep.BrokenAt, rep.Err = line, "entry hash mismatch"
			return rep, nil
		}
		prev = e.Hash
	}
	return rep, sc.Err()
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
