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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/auditchain"
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
	mu      sync.Mutex
	chain   *auditchain.Writer
	surface string
	session func() string
	tenant  func() string
	usage   func() *models.UsageInfo
	logger  *zap.Logger
}

// initLLMAudit installs the request auditor when the audit path is
// configured. surface names the process role for the trail (repl, oneshot,
// gateway, mcp, ...). Failures are logged and leave auditing off — the
// trail must never block a session from starting.
func (cli *ChatCLI) initLLMAudit(surface string) {
	if err := cli.initLLMAuditErr(surface); err != nil {
		// A locked managed audit policy fails CLOSED: the operator pinned
		// the trail and it cannot be written, so the session must not
		// run unaudited.
		fmt.Fprintln(os.Stderr, colorize("  "+i18n.T("audit.locked_unavailable", err), ColorRed))
		os.Exit(1)
	}
}

// errAuditLocked marks a locked managed audit path that cannot be opened.
var errAuditLocked = errors.New("locked audit path unavailable")

// auditPathLocked reports whether the audit path is pinned by managed.env.
func auditPathLocked() bool {
	entry, managed := config.ManagedEntryFor(AuditLogPathEnv)
	return managed && entry.Locked
}

// initLLMAuditErr installs the auditor; the error is non-nil only when the
// managed policy locks the path and the trail cannot be opened (an
// unlocked path that fails still logs and leaves auditing off).
func (cli *ChatCLI) initLLMAuditErr(surface string) error {
	path := os.Getenv(AuditLogPathEnv)
	if path == "" {
		return nil
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		if cli.logger != nil {
			cli.logger.Error("audit log path must be absolute; LLM audit disabled", zap.String("path", path))
		}
		if auditPathLocked() {
			return fmt.Errorf("%w: %s is not absolute", errAuditLocked, path)
		}
		return nil
	}
	chain, err := auditchain.Open(clean, auditchain.Options{})
	if err != nil {
		if cli.logger != nil {
			cli.logger.Error("failed to open audit log for LLM requests", zap.String("path", clean), zap.Error(err))
		}
		if auditPathLocked() {
			return fmt.Errorf("%w: %s: %s", errAuditLocked, clean, err.Error())
		}
		return nil
	}
	w := &llmAuditWriter{chain: chain, surface: surface, logger: cli.logger}
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
	return nil
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
	if cli == nil {
		return
	}
	cli.telemetrySurface(surface)
	if cli.llmAudit == nil {
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
	// The chain writer assigns seq/prev_hash/hash under the file lock, so
	// several processes (REPL, gateway, gRPC server) share one trail.
	if err := w.chain.Append(entry); err != nil && w.logger != nil {
		w.logger.Warn("audit log write failed", zap.Error(err))
	}
}

// auditEntryHash computes the version-1 chain hash of an entry (its Hash
// field excluded, PrevHash included): struct-order canonical JSON. Kept
// to verify trails written before the shared chain writer.
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

// verifyLegacyAuditLine verifies one version-1 line (struct-order hash).
func verifyLegacyAuditLine(line []byte, prev string) (string, bool) {
	var e llmAuditEntry
	if err := json.Unmarshal(line, &e); err != nil || e.PrevHash != prev {
		return "", false
	}
	if auditEntryHash(e) != e.Hash {
		return "", false
	}
	return e.Hash, true
}

// AuditChainReport is the outcome of VerifyAuditChain.
type AuditChainReport struct {
	Entries  int
	Chained  int // entries carrying a hash
	Legacy   int // entries written before the chain existed
	BrokenAt int // 1-based line of the first break (0 = intact)
	Err      string
	// Sealed counts entries stored encrypted; Torn flags an incomplete
	// last line (a crash mid-write, not tampering); RotatedFrom names the
	// file this trail continues when it starts mid-chain.
	Sealed      int
	Torn        bool
	RotatedFrom string
}

// Intact reports whether every chained entry verified.
func (r AuditChainReport) Intact() bool { return r.BrokenAt == 0 }

// VerifyAuditChain re-hashes a trail and reports the first line whose hash
// or previous-hash link does not match. Version-1 lines (written before
// the shared chain writer) verify with the struct-order hash.
func VerifyAuditChain(path string) (AuditChainReport, error) {
	rep, err := auditchain.Verify(path, verifyLegacyAuditLine)
	return AuditChainReport{
		Entries: rep.Entries, Chained: rep.Chained, Legacy: rep.Legacy, BrokenAt: rep.BrokenAt, Err: rep.Err,
		Sealed: rep.Sealed, Torn: rep.Torn, RotatedFrom: rep.RotatedFrom,
	}, err
}

// close detaches the sink and closes the file.
func (w *llmAuditWriter) close() {
	if w == nil {
		return
	}
	client.RegisterRequestAuditor(nil)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.chain != nil {
		_ = w.chain.Close()
		w.chain = nil
	}
}
