/*
 * ChatCLI - MCP Channel Manager
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Production-grade push-message ring for MCP servers:
 *
 *   - Bounded in-memory ring (Push / GetRecent / GetByChannel / Count)
 *     keeps the working set hot and lock-friendly.
 *   - Optional append-only JSONL persistence (~/.chatcli/channels.jsonl)
 *     with size-bounded rotation so a server that fires for days
 *     does not grow the file forever. Writes are best-effort: an
 *     I/O error logs at warn but never blocks Push, because the
 *     ring is the source of truth for the live session.
 *   - Per-server channel allow-listing — when a ServerConfig declares
 *     `channels: [...]`, only messages on listed channels are kept.
 *     Empty/missing list means "accept everything".
 *   - Unread tracking so the prompt footer can render a counter and
 *     /channel ack can clear it without dropping history.
 *   - OnMessage subscriber fan-out so the trigger engine can react
 *     in real time without polling the ring.
 *
 * The manager is process-local and concurrency-safe. Every public
 * method that touches state takes an RW lock; subscriber dispatch
 * runs unlocked with a copied slice to keep handler latency from
 * holding up the next Push.
 */
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Defaults tuned for the inbox-style use case: hundreds of alerts a
// day comfortably fit, rotation kicks in well before any single
// file grows large enough to slow down disk I/O on cold boot, and
// the load window is generous enough to survive a chatcli restart
// in the middle of a busy CI run without losing context.
const (
	defaultRingCapacity     = 200
	defaultPersistMaxBytes  = 10 << 20 // 10 MiB before rotation
	defaultPersistLoadLimit = 200      // most recent N replayed on startup
	persistFileName         = "channels.jsonl"
	persistRotatedSuffix    = ".1"
)

// ChannelMessage represents a push message from an MCP server.
//
// Wire-stable: the JSON shape is what gets written to the persistence
// file and what tests assert against, so adding fields must stay
// additive (omitempty + safe zero value).
type ChannelMessage struct {
	ServerName string            `json:"serverName"`
	Channel    string            `json:"channel"`
	Content    string            `json:"content"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Timestamp  time.Time         `json:"timestamp"`
	// Seq is a monotonically increasing identifier assigned on Push,
	// unique per ChannelManager instance. The CLI uses it to address
	// individual events (`/channel run <seq>`) and the trigger engine
	// uses it for dedup so the same wire message never fires twice.
	Seq uint64 `json:"seq"`
}

// ChannelHandler is called when a channel message is received.
// Handlers run on the Push goroutine after the ring has been updated,
// so they MUST NOT block — long-running work should hand off to its
// own goroutine.
type ChannelHandler func(msg ChannelMessage)

// ChannelSubscriptionResolver lets the ChannelManager check whether
// a (server, channel) pair is opted into delivery. Provided by the
// MCP Manager so the ChannelManager does not need to know about
// ServerConfig directly — keeps the dependency arrow one-way.
type ChannelSubscriptionResolver func(serverName, channel string) bool

// ChannelManager manages MCP channel subscriptions and message delivery.
type ChannelManager struct {
	mu             sync.RWMutex
	messages       []ChannelMessage
	handlers       []ChannelHandler
	maxMessages    int
	unread         int
	lastViewedSeq  uint64
	seq            atomic.Uint64
	logger         *zap.Logger
	resolver       ChannelSubscriptionResolver
	persistPath    string
	persistMaxSize int64
	persistMu      sync.Mutex // serializes file writes / rotation
	persistFile    *os.File
	persistFailed  atomic.Bool // latched after a write failure to suppress spam
	stopOnce       sync.Once
	closed         atomic.Bool
}

// ChannelManagerOptions configures non-default behavior at construction time.
// All fields are optional; zero-value falls back to documented defaults.
type ChannelManagerOptions struct {
	// MaxMessages caps the in-memory ring. Zero → defaultRingCapacity.
	MaxMessages int
	// PersistDir is the directory where channels.jsonl is written.
	// Empty disables persistence entirely; tests pass t.TempDir() to
	// keep state isolated.
	PersistDir string
	// PersistMaxBytes triggers rotation when the active file grows
	// past this size. Zero → defaultPersistMaxBytes.
	PersistMaxBytes int64
	// LoadLimit caps how many recent messages are replayed from disk
	// on startup. Zero → defaultPersistLoadLimit. Negative disables
	// the load step entirely (rare; useful for tests that want a
	// guaranteed-empty ring).
	LoadLimit int
}

// NewChannelManager creates a manager with default options.
// Persistence is disabled; callers that want durability use
// NewChannelManagerWithOptions and pass a PersistDir.
func NewChannelManager(logger *zap.Logger) *ChannelManager {
	return NewChannelManagerWithOptions(logger, ChannelManagerOptions{})
}

// NewChannelManagerWithOptions constructs a manager with explicit options
// and loads any prior state from disk when PersistDir is set.
func NewChannelManagerWithOptions(logger *zap.Logger, opts ChannelManagerOptions) *ChannelManager {
	maxMsgs := opts.MaxMessages
	if maxMsgs <= 0 {
		maxMsgs = defaultRingCapacity
	}
	maxSize := opts.PersistMaxBytes
	if maxSize <= 0 {
		maxSize = defaultPersistMaxBytes
	}
	cm := &ChannelManager{
		maxMessages:    maxMsgs,
		logger:         logger,
		persistMaxSize: maxSize,
	}
	if opts.PersistDir != "" {
		cm.persistPath = filepath.Join(opts.PersistDir, persistFileName)
		limit := opts.LoadLimit
		if limit == 0 {
			limit = defaultPersistLoadLimit
		}
		if limit > 0 {
			cm.loadFromDisk(limit)
		}
		if err := cm.openPersistFile(); err != nil {
			cm.logger.Warn("MCP channels persistence disabled (open failed)",
				zap.String("path", cm.persistPath),
				zap.Error(err))
			cm.persistPath = ""
		}
	}
	return cm
}

// SetSubscriptionResolver installs a callback that gates delivery by
// per-server channel allow-list. nil disables filtering (accept all).
// Safe to call after construction; the next Push picks it up.
func (cm *ChannelManager) SetSubscriptionResolver(resolver ChannelSubscriptionResolver) {
	cm.mu.Lock()
	cm.resolver = resolver
	cm.mu.Unlock()
}

// OnMessage registers a handler that will be called for each delivered
// channel message (after filtering). Handlers receive a copy so they
// can retain the message without aliasing the ring.
func (cm *ChannelManager) OnMessage(handler ChannelHandler) {
	if handler == nil {
		return
	}
	cm.mu.Lock()
	cm.handlers = append(cm.handlers, handler)
	cm.mu.Unlock()
}

// Push processes an incoming channel message from an MCP server.
// Drops the message silently if the resolver rejects it (logging at
// debug for visibility under --debug).
func (cm *ChannelManager) Push(msg ChannelMessage) {
	if cm.closed.Load() {
		return
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}
	if msg.Channel == "" {
		msg.Channel = "default"
	}

	cm.mu.RLock()
	resolver := cm.resolver
	cm.mu.RUnlock()
	if resolver != nil && !resolver(msg.ServerName, msg.Channel) {
		cm.logger.Debug("MCP channel message dropped by subscription filter",
			zap.String("server", msg.ServerName),
			zap.String("channel", msg.Channel))
		return
	}

	msg.Seq = cm.seq.Add(1)

	cm.mu.Lock()
	cm.messages = append(cm.messages, msg)
	if len(cm.messages) > cm.maxMessages {
		cm.messages = cm.messages[len(cm.messages)-cm.maxMessages:]
	}
	cm.unread++
	handlers := make([]ChannelHandler, len(cm.handlers))
	copy(handlers, cm.handlers)
	cm.mu.Unlock()

	cm.logger.Info("MCP channel message received",
		zap.String("server", msg.ServerName),
		zap.String("channel", msg.Channel),
		zap.Uint64("seq", msg.Seq),
		zap.Int("content_len", len(msg.Content)))

	cm.persist(msg)

	for _, h := range handlers {
		h(msg)
	}
}

// GetRecent returns the N most recent channel messages in chronological
// order (oldest first). Caller-owned copy — safe to mutate.
func (cm *ChannelManager) GetRecent(n int) []ChannelMessage {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if n <= 0 || len(cm.messages) == 0 {
		return nil
	}
	if n > len(cm.messages) {
		n = len(cm.messages)
	}
	out := make([]ChannelMessage, n)
	copy(out, cm.messages[len(cm.messages)-n:])
	return out
}

// GetByChannel returns the N most recent messages whose channel matches
// `channel`. The literal "*" matches every channel (so /channel * acts
// as a synonym for the global feed).
func (cm *ChannelManager) GetByChannel(channel string, n int) []ChannelMessage {
	if n <= 0 {
		return nil
	}
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	filtered := make([]ChannelMessage, 0, n)
	for i := len(cm.messages) - 1; i >= 0 && len(filtered) < n; i-- {
		if cm.messages[i].Channel == channel || channel == "*" {
			filtered = append(filtered, cm.messages[i])
		}
	}
	for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
		filtered[i], filtered[j] = filtered[j], filtered[i]
	}
	return filtered
}

// GetBySeq returns the message with the given seq, plus a boolean
// indicating whether it was found. Used by /channel run <seq> to
// pinpoint the exact event to act on.
func (cm *ChannelManager) GetBySeq(seq uint64) (ChannelMessage, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	for i := len(cm.messages) - 1; i >= 0; i-- {
		if cm.messages[i].Seq == seq {
			return cm.messages[i], true
		}
	}
	return ChannelMessage{}, false
}

// Count returns the total number of messages currently held in the ring.
func (cm *ChannelManager) Count() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.messages)
}

// Unread reports how many messages have arrived since the last call
// to Ack (or since startup, whichever is more recent).
func (cm *ChannelManager) Unread() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.unread
}

// Ack clears the unread counter and remembers the most recent seq as
// "viewed". Returns the number of messages that were unread at the
// moment of the call so callers can render a one-line summary.
func (cm *ChannelManager) Ack() int {
	cm.mu.Lock()
	cleared := cm.unread
	cm.unread = 0
	if n := len(cm.messages); n > 0 {
		cm.lastViewedSeq = cm.messages[n-1].Seq
	}
	cm.mu.Unlock()
	return cleared
}

// UnreadSince returns the messages received after the last Ack. Used
// by the prompt-cycle hook so the user only sees the new events on
// the inbox banner — not the entire history.
func (cm *ChannelManager) UnreadSince() []ChannelMessage {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.unread == 0 || len(cm.messages) == 0 {
		return nil
	}
	start := len(cm.messages) - cm.unread
	if start < 0 {
		start = 0
	}
	out := make([]ChannelMessage, len(cm.messages)-start)
	copy(out, cm.messages[start:])
	return out
}

// FormatForPrompt renders the most recent N messages as a single
// system-prompt block, suitable for injection into the LLM turn. An
// empty ring returns "" so the caller can omit the block.
func (cm *ChannelManager) FormatForPrompt(maxMessages int) string {
	recent := cm.GetRecent(maxMessages)
	if len(recent) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## MCP Channel Messages (Recent)\n\n")
	for _, msg := range recent {
		sb.WriteString(fmt.Sprintf("[%s/%s %s] %s\n",
			msg.ServerName, msg.Channel,
			msg.Timestamp.Format("15:04:05"),
			msg.Content))
	}
	return sb.String()
}

// ProcessSSENotification handles a JSON-RPC notification body received
// from an MCP server transport. Recognized shapes:
//
//   - method = "notifications/<channel>"   → routed to that channel
//   - method = "message" | "channel/message" → params has {channel,
//     content|text|message}; channel defaults to "default"
//   - any other JSON-RPC method → channel = method, content = params
//   - anything that does not parse as JSON-RPC → channel = "raw"
//
// ServerName is captured from the transport so cross-server alerts
// stay distinguishable in the ring.
func (cm *ChannelManager) ProcessSSENotification(serverName string, data []byte) {
	var notification struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(data, &notification); err != nil {
		cm.Push(ChannelMessage{
			ServerName: serverName,
			Channel:    "raw",
			Content:    string(data),
		})
		return
	}

	switch {
	case strings.HasPrefix(notification.Method, "notifications/"):
		channel := strings.TrimPrefix(notification.Method, "notifications/")
		content := strings.TrimSpace(string(notification.Params))
		if content == "" || content == "null" {
			content = notification.Method
		}
		cm.Push(ChannelMessage{
			ServerName: serverName,
			Channel:    channel,
			Content:    content,
		})

	case notification.Method == "message" || notification.Method == "channel/message":
		var payload struct {
			Channel  string            `json:"channel"`
			Content  string            `json:"content"`
			Text     string            `json:"text"`
			Message  string            `json:"message"`
			Metadata map[string]string `json:"metadata"`
		}
		_ = json.Unmarshal(notification.Params, &payload)
		content := firstNonEmpty(payload.Content, payload.Text, payload.Message, strings.TrimSpace(string(notification.Params)))
		channel := payload.Channel
		if channel == "" {
			channel = "default"
		}
		cm.Push(ChannelMessage{
			ServerName: serverName,
			Channel:    channel,
			Content:    content,
			Metadata:   payload.Metadata,
		})

	default:
		cm.Push(ChannelMessage{
			ServerName: serverName,
			Channel:    notification.Method,
			Content:    strings.TrimSpace(string(notification.Params)),
		})
	}
}

// Close releases the persistence file. Safe to call multiple times.
// Subsequent Push calls become no-ops once Close has run, so the
// CLI can call this from its shutdown path without coordinating with
// the transports — any late notifications during teardown are dropped
// instead of racing with the file close.
func (cm *ChannelManager) Close() error {
	var err error
	cm.stopOnce.Do(func() {
		cm.closed.Store(true)
		cm.persistMu.Lock()
		if cm.persistFile != nil {
			err = cm.persistFile.Close()
			cm.persistFile = nil
		}
		cm.persistMu.Unlock()
	})
	return err
}

// firstNonEmpty returns the first non-empty argument or "" if all
// are empty. Used to coalesce the many ways MCP servers in the wild
// label their payload body.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// loadFromDisk replays up to limit most recent records from the
// persistence file. Corruption in any single line is logged and
// skipped — one malformed entry never poisons the rest of the load.
// Rotated files (.1) are read first so chronological order is
// preserved across rotations.
func (cm *ChannelManager) loadFromDisk(limit int) {
	rotated := cm.persistPath + persistRotatedSuffix
	var lines []string
	lines = append(lines, readTailLines(cm.logger, rotated, limit)...)
	lines = append(lines, readTailLines(cm.logger, cm.persistPath, limit)...)
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}

	loaded := make([]ChannelMessage, 0, len(lines))
	for _, line := range lines {
		var msg ChannelMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			cm.logger.Debug("MCP channels: skipping corrupted persistence line",
				zap.Error(err))
			continue
		}
		loaded = append(loaded, msg)
	}
	if len(loaded) == 0 {
		return
	}

	cm.mu.Lock()
	cm.messages = append(cm.messages, loaded...)
	if len(cm.messages) > cm.maxMessages {
		cm.messages = cm.messages[len(cm.messages)-cm.maxMessages:]
	}
	// Seed the seq counter past the highest observed value so new
	// arrivals do not collide with the replayed history. Persisted
	// messages count as "already seen" — Unread reflects only what
	// arrives during this process's lifetime.
	var maxSeq uint64
	for _, m := range loaded {
		if m.Seq > maxSeq {
			maxSeq = m.Seq
		}
	}
	cm.seq.Store(maxSeq)
	cm.lastViewedSeq = maxSeq
	cm.mu.Unlock()

	cm.logger.Info("MCP channels persistence loaded",
		zap.Int("messages", len(loaded)),
		zap.String("path", cm.persistPath))
}

// readTailLines returns the last up-to-limit lines of a file in
// chronological order. Returns nil and logs at debug when the file
// does not exist (cold start) or cannot be opened.
func readTailLines(logger *zap.Logger, path string, limit int) []string {
	f, err := os.Open(path) //#nosec G304 -- caller-controlled persistence path inside ~/.chatcli
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Debug("MCP channels: persistence open failed",
				zap.String("path", path),
				zap.Error(err))
		}
		return nil
	}
	defer f.Close() //nolint:errcheck // read-only file, close error is informational

	// Use a ring of size limit so we never have to read the whole
	// file into memory. JSONL with one record per line keeps this
	// simple — no multi-line records to stitch back together.
	ring := make([]string, 0, limit)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if len(ring) < limit {
			ring = append(ring, line)
			continue
		}
		copy(ring, ring[1:])
		ring[len(ring)-1] = line
	}
	if err := scanner.Err(); err != nil {
		logger.Debug("MCP channels: persistence scan error",
			zap.String("path", path),
			zap.Error(err))
	}
	return ring
}

// openPersistFile opens the persistence file in append-only mode.
// Creates the parent directory if needed.
func (cm *ChannelManager) openPersistFile() error {
	if cm.persistPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(cm.persistPath), 0o700); err != nil {
		return fmt.Errorf("create persistence dir: %w", err)
	}
	f, err := os.OpenFile(cm.persistPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //#nosec G304 -- caller-controlled persistence path inside ~/.chatcli
	if err != nil {
		return fmt.Errorf("open persistence file: %w", err)
	}
	cm.persistFile = f
	return nil
}

// persist appends one message to the JSONL file. Best-effort: any
// failure latches persistFailed to suppress further log spam and
// the in-memory ring continues serving traffic. Rotation runs
// inline so the file never grows unboundedly between writes.
func (cm *ChannelManager) persist(msg ChannelMessage) {
	if cm.persistPath == "" || cm.persistFailed.Load() {
		return
	}
	line, err := json.Marshal(msg)
	if err != nil {
		cm.logger.Debug("MCP channels: marshal failed (skipping persistence)",
			zap.Error(err))
		return
	}
	line = append(line, '\n')

	cm.persistMu.Lock()
	defer cm.persistMu.Unlock()

	if cm.persistFile == nil {
		return
	}
	if _, err := cm.persistFile.Write(line); err != nil {
		cm.logger.Warn("MCP channels: persistence write failed (disabling further writes)",
			zap.String("path", cm.persistPath),
			zap.Error(err))
		cm.persistFailed.Store(true)
		return
	}
	cm.maybeRotateLocked()
}

// maybeRotateLocked rotates the persistence file when it exceeds
// persistMaxSize. Must be called with persistMu held. Atomic swap:
// the active file is renamed to .1 (replacing any previous backup)
// and a fresh file is opened in its place. On any error we keep
// using the existing file — rotation is best-effort.
func (cm *ChannelManager) maybeRotateLocked() {
	stat, err := cm.persistFile.Stat()
	if err != nil {
		return
	}
	if stat.Size() < cm.persistMaxSize {
		return
	}
	rotated := cm.persistPath + persistRotatedSuffix
	if err := cm.persistFile.Close(); err != nil {
		cm.logger.Warn("MCP channels: close before rotate failed",
			zap.Error(err))
		// Try to reopen — if that also fails we'll latch in the
		// next write.
		cm.persistFile, _ = os.OpenFile(cm.persistPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //#nosec G304 -- caller-controlled persistence path inside ~/.chatcli
		return
	}
	// Drop any previous rotated copy before renaming so we are not
	// at the mercy of platform-specific rename-over-existing
	// semantics (Windows pre-Vista required Delete first; macOS and
	// Linux are atomic, but we keep the explicit Remove for
	// symmetry).
	if err := os.Remove(rotated); err != nil && !os.IsNotExist(err) {
		cm.logger.Warn("MCP channels: removing old rotated file failed",
			zap.String("path", rotated),
			zap.Error(err))
	}
	if err := os.Rename(cm.persistPath, rotated); err != nil {
		cm.logger.Warn("MCP channels: rotation rename failed",
			zap.String("from", cm.persistPath),
			zap.String("to", rotated),
			zap.Error(err))
	}
	f, err := os.OpenFile(cm.persistPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //#nosec G304 -- caller-controlled persistence path inside ~/.chatcli
	if err != nil {
		cm.logger.Warn("MCP channels: reopen after rotate failed (disabling persistence)",
			zap.Error(err))
		cm.persistFailed.Store(true)
		cm.persistFile = nil
		return
	}
	cm.persistFile = f
	cm.logger.Info("MCP channels: persistence file rotated",
		zap.String("path", cm.persistPath),
		zap.String("rotated", rotated))
}

// Ensure io interface compliance helpers stay referenced even when
// the package builds without persistence callers in some test paths.
var _ io.Closer = (*ChannelManager)(nil)
