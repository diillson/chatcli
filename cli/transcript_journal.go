/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Append-only transcript journal.
 *
 * Saved sessions persist the in-memory window as it stands — after
 * compaction the original messages exist only as CCR keys with a 7-day TTL,
 * and a hard kill mid-turn lost everything since the last turn boundary.
 * The journal is the durable record: every message is appended once, the
 * first time it appears at the tail of the live history, and a history
 * rewrite (compaction, guided /compact, microcompact stubs) is recorded as
 * an event instead of being lost. Synced at turn boundaries and after each
 * agent tool batch, each line fsynced, sealed line by line when encryption
 * at rest is enabled. Journals follow the machine-session TTL.
 */
package cli

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/atrest"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	// TranscriptEnv disables the journal when set to false.
	TranscriptEnv = "CHATCLI_SESSION_TRANSCRIPT"
	// transcriptDirName is the store under ~/.chatcli.
	transcriptDirName = "transcripts"
	// sealedLinePrefix marks a journal line encrypted at rest.
	sealedLinePrefix = "enc:"
)

// transcriptEvent is one journal line.
type transcriptEvent struct {
	TS      time.Time       `json:"ts"`
	Kind    string          `json:"kind"` // "msg" | "rewrite"
	Message *models.Message `json:"message,omitempty"`
	Before  int             `json:"before,omitempty"` // rewrite: messages before
	After   int             `json:"after,omitempty"`  // rewrite: messages after
	// Hashes (rewrite events) is the ordered hash list of the history the
	// rewrite replaced, so the pre-rewrite conversation can be rebuilt
	// from the journaled messages (/rewind compact after a resume).
	Hashes []string `json:"hashes,omitempty"`
}

// transcriptJournal appends the live history to one file per session.
type transcriptJournal struct {
	mu         sync.Mutex
	id         string
	path       string
	lastCount  int
	lastHash   string          // hash of the last journaled message
	lastHashes []string        // ordered hashes of the last synced history
	seen       map[string]bool // hashes journaled so far (rewrite dedup)
	disabled   bool
}

// transcriptEnabled honors CHATCLI_SESSION_TRANSCRIPT (default on).
func transcriptEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(TranscriptEnv)))
	return !(v == "false" || v == "0" || v == "off" || v == "no")
}

// newTranscriptID mints a sortable, collision-safe journal id.
func newTranscriptID(now time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "tr-" + now.Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// transcriptDir returns the journal directory, creating it.
func transcriptDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".chatcli", transcriptDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// openTranscriptJournal binds a journal to id under dir. An existing file
// (a resumed session) is scanned so already-journaled messages are not
// appended again.
func openTranscriptJournal(dir, id string) (*transcriptJournal, error) {
	if id == "" || id != filepath.Base(id) {
		return nil, fmt.Errorf("transcript: invalid id %q", id)
	}
	j := &transcriptJournal{id: id, path: filepath.Join(dir, id+".jsonl"), seen: map[string]bool{}}
	msgs, err := readTranscript(j.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, m := range msgs {
		h := messageHash(m)
		j.seen[h] = true
		j.lastHash = h
	}
	j.lastCount = 0 // the live history is re-synced from scratch by hash
	return j, nil
}

// messageHash identifies a message by role, content, tool-result id,
// every native tool call (id, name, arguments) and the image count, so two
// assistant messages that differ only in the call they made — the common
// shape in a tool loop — never collapse into one journal entry.
func messageHash(m models.Message) string {
	h := sha256.New()
	h.Write([]byte(m.Role))
	h.Write([]byte{0})
	h.Write([]byte(m.Content))
	h.Write([]byte{0})
	h.Write([]byte(m.ToolCallID))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(len(m.ToolCalls))))
	for _, tc := range m.ToolCalls {
		h.Write([]byte{0})
		h.Write([]byte(tc.ID))
		h.Write([]byte{0})
		h.Write([]byte(tc.Name))
		h.Write([]byte{0})
		h.Write([]byte(tc.ArgumentsJSON()))
	}
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(len(m.Images))))
	return hex.EncodeToString(h.Sum(nil))
}

// hashHistory hashes every message of a history in order.
func hashHistory(history []models.Message) []string {
	out := make([]string, len(history))
	for i := range history {
		out[i] = messageHash(history[i])
	}
	return out
}

// prefixMatches reports whether hashes starts with prefix.
func prefixMatches(hashes, prefix []string) bool {
	if len(hashes) < len(prefix) {
		return false
	}
	for i := range prefix {
		if hashes[i] != prefix[i] {
			return false
		}
	}
	return true
}

// Sync appends every message that has not been journaled yet. When the
// history no longer extends the journaled prefix (a rewrite), a rewrite
// event is recorded and only genuinely new messages are appended.
func (j *transcriptJournal) Sync(history []models.Message) error {
	if j == nil || j.disabled {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	// The whole prefix is compared, not just the last message: an in-place
	// mutation (microcompact, read dedup, pairing repair, Level 1 trim)
	// leaves the tail intact while changing earlier messages, and a journal
	// that only watched the tail recorded nothing — undo and checkpoints
	// then resolved against hashes that were never appended.
	hashes := hashHistory(history)
	extends := j.lastCount > 0 && prefixMatches(hashes, j.lastHashes)
	var events []transcriptEvent
	now := time.Now()
	if !extends && j.lastCount > 0 {
		events = append(events, transcriptEvent{TS: now, Kind: "rewrite", Before: j.lastCount, After: len(history), Hashes: append([]string(nil), j.lastHashes...)})
	}
	start := 0
	if extends {
		start = j.lastCount
	}
	for i := start; i < len(history); i++ {
		h := hashes[i]
		// A genuinely new tail message is always journaled, even when its
		// bytes match an earlier one ("ok", "continue"): the transcript is
		// a record, not a set. Dedup applies only when a rewrite re-emits
		// messages the journal already holds.
		if !extends && j.seen[h] {
			continue
		}
		m := history[i]
		events = append(events, transcriptEvent{TS: now, Kind: "msg", Message: &m})
		j.seen[h] = true
	}
	if len(events) > 0 {
		if err := j.appendEvents(events); err != nil {
			return err
		}
	}
	j.lastCount = len(history)
	j.lastHashes = hashes
	if len(history) > 0 {
		j.lastHash = hashes[len(history)-1]
	} else {
		j.lastHash = ""
	}
	return nil
}

// appendEvents writes events as JSON lines (sealed when encryption at rest
// is on) and fsyncs.
func (j *transcriptJournal) appendEvents(events []transcriptEvent) error {
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600) // #nosec G304 -- path built from a validated id under ~/.chatcli/transcripts
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	// A crash mid-append leaves a partial last line; start on a fresh line
	// so the reader can skip the torn one and every later event stays
	// readable.
	if info, serr := f.Stat(); serr == nil && info.Size() > 0 {
		tail := make([]byte, 1)
		if _, rerr := f.ReadAt(tail, info.Size()-1); rerr == nil && tail[0] != '\n' {
			_, _ = w.WriteString("\n")
		}
	}
	for _, ev := range events {
		line, err := json.Marshal(ev)
		if err != nil {
			_ = f.Close()
			return err
		}
		if atrest.Enabled() {
			sealed, err := atrest.Seal(line)
			if err != nil {
				_ = f.Close()
				return err
			}
			line = []byte(sealedLinePrefix + base64.StdEncoding.EncodeToString(sealed))
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// readTranscript returns every journaled message in order (rewrite events
// are skipped: the journal is the full record, not the compacted view).
func readTranscript(path string) ([]models.Message, error) {
	events, err := readTranscriptEvents(path)
	if err != nil {
		return nil, err
	}
	out := make([]models.Message, 0, len(events))
	for _, ev := range events {
		if ev.Kind == "msg" && ev.Message != nil {
			out = append(out, *ev.Message)
		}
	}
	return out, nil
}

// transcriptIndex maps message hash → message over the journaled
// messages (first occurrence wins; the journal dedups on append anyway).
func transcriptIndex(events []transcriptEvent) map[string]models.Message {
	idx := make(map[string]models.Message, len(events))
	for _, ev := range events {
		if ev.Kind != "msg" || ev.Message == nil {
			continue
		}
		h := messageHash(*ev.Message)
		if _, ok := idx[h]; !ok {
			idx[h] = *ev.Message
		}
	}
	return idx
}

// resolveHashes rebuilds a history from ordered hashes; ok is false when
// any hash is missing from the index (the journal was pruned or rotated).
func resolveHashes(idx map[string]models.Message, hashes []string) ([]models.Message, bool) {
	out := make([]models.Message, 0, len(hashes))
	for _, h := range hashes {
		m, ok := idx[h]
		if !ok {
			return nil, false
		}
		out = append(out, m)
	}
	return out, true
}

// transcriptEvents reads the active journal's events ("" path when the
// journal is off).
func (cli *ChatCLI) transcriptEvents() ([]transcriptEvent, error) {
	if cli == nil || cli.transcript == nil || cli.transcript.disabled || cli.transcript.path == "" {
		return nil, os.ErrNotExist
	}
	return readTranscriptEvents(cli.transcript.path)
}

// readTranscriptEvents returns every journal event in order.
func readTranscriptEvents(path string) ([]transcriptEvent, error) {
	f, err := os.Open(path) // #nosec G304 -- journal path under ~/.chatcli/transcripts
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	events, _, err := decodeTranscriptEvents(f)
	return events, err
}

// decodeTranscriptEvents reads journal lines with no line-length cap and
// skips what it cannot use — a torn last line from a crash, a corrupt or
// foreign line — instead of failing the whole journal (which used to
// disable undo, checkpoints and export for the session). A sealed line
// this process cannot open is an error: that is a key problem, not a
// corrupt journal. Returns the events and how many lines were skipped.
func decodeTranscriptEvents(r io.Reader) ([]transcriptEvent, int, error) {
	br := bufio.NewReaderSize(r, 1024*1024)
	var out []transcriptEvent
	skipped := 0
	for {
		line, rerr := br.ReadBytes('\n')
		torn := rerr != nil && len(line) > 0 // no trailing newline: partial write
		line = bytes.TrimRight(line, "\r\n")
		if len(line) > 0 && !torn {
			ev, ok, oerr := decodeTranscriptLine(line)
			if oerr != nil {
				return out, skipped, oerr
			}
			if ok {
				out = append(out, ev)
			} else {
				skipped++
			}
		} else if torn {
			skipped++
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return out, skipped, nil
			}
			return out, skipped, rerr
		}
	}
}

// decodeTranscriptLine parses one line; ok is false for a line that is not
// a journal event.
func decodeTranscriptLine(line []byte) (transcriptEvent, bool, error) {
	if bytes.HasPrefix(line, []byte(sealedLinePrefix)) {
		raw, err := base64.StdEncoding.DecodeString(string(line[len(sealedLinePrefix):]))
		if err != nil {
			return transcriptEvent{}, false, nil
		}
		plain, err := atrest.Open(raw)
		if err != nil {
			return transcriptEvent{}, false, err
		}
		line = plain
	}
	var ev transcriptEvent
	if err := json.Unmarshal(line, &ev); err != nil || (ev.Kind != "msg" && ev.Kind != "rewrite") {
		return transcriptEvent{}, false, nil
	}
	return ev, true, nil
}

// pruneTranscripts removes journals whose last write is older than ttl.
// Zero ttl keeps everything. Returns how many were removed.
func pruneTranscripts(dir string, ttl time.Duration) int {
	if ttl <= 0 {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-ttl)
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(dir, e.Name())) == nil {
			removed++
		}
	}
	return removed
}

// transcriptTTL mirrors the machine-session policy (see sessionTTLDuration
// in retention.go): CHATCLI_SESSION_TTL in days, 90 by default, 0 disables
// expiry.
func transcriptTTL() time.Duration {
	return sessionTTLDuration()
}

// transcriptDirForRoot resolves the journal directory under the active
// state root (per-tenant store sets rebase it), defaulting to ~/.chatcli.
func (cli *ChatCLI) transcriptDirForRoot() (string, error) {
	if cli != nil && cli.stateRoot != "" {
		dir := filepath.Join(cli.stateRoot, transcriptDirName)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		return dir, nil
	}
	return transcriptDir()
}

// --- ChatCLI wiring ---

// initTranscriptJournal opens the session journal at boot (or adopts an
// existing id when a saved session that carries one is loaded).
func (cli *ChatCLI) initTranscriptJournal(id string) {
	if !transcriptEnabled() {
		return
	}
	dir, err := cli.transcriptDirForRoot()
	if err != nil {
		if cli.logger != nil {
			cli.logger.Warn("transcript journal unavailable", zap.Error(err))
		}
		return
	}
	if id == "" {
		id = newTranscriptID(time.Now())
	}
	j, err := openTranscriptJournal(dir, id)
	if err != nil {
		if cli.logger != nil {
			cli.logger.Warn("transcript journal could not be opened", zap.Error(err))
		}
		return
	}
	cli.transcript = j
	if removed := pruneTranscripts(dir, transcriptTTL()); removed > 0 && cli.logger != nil {
		cli.logger.Info("expired transcript journals pruned", zap.Int("removed", removed))
	}
}

// syncTranscript journals whatever the live history gained since the last
// call. Errors are logged, never surfaced: the journal must not break a turn.
func (cli *ChatCLI) syncTranscript() {
	if cli == nil || cli.transcript == nil {
		return
	}
	if err := cli.transcript.Sync(cli.history); err != nil && cli.logger != nil {
		cli.logger.Warn("transcript journal append failed", zap.Error(err))
	}
}

// transcriptID returns the active journal id ("" when disabled).
func (cli *ChatCLI) transcriptID() string {
	if cli == nil || cli.transcript == nil {
		return ""
	}
	return cli.transcript.id
}

// forkTranscriptJournal copies the live journal to a fresh id (the fork's
// own timeline, seeded with the parent's events) and returns that id;
// "" when journaling is off. The parent keeps writing to its own file.
func (cli *ChatCLI) forkTranscriptJournal() string {
	if cli == nil || cli.transcript == nil || cli.transcript.path == "" {
		return ""
	}
	id := newTranscriptID(time.Now())
	src := cli.transcript.path
	dst := filepath.Join(filepath.Dir(src), id+filepath.Ext(src))
	data, err := os.ReadFile(src) // #nosec G304 -- our own journal path under the state root
	if err != nil && !os.IsNotExist(err) {
		if cli.logger != nil {
			cli.logger.Warn("transcript fork: cannot read the parent journal", zap.Error(err))
		}
		return ""
	}
	if err := utils.AtomicWriteFile(dst, data, 0o600); err != nil {
		if cli.logger != nil {
			cli.logger.Warn("transcript fork: cannot seed the new journal", zap.Error(err))
		}
		return ""
	}
	return id
}

// adoptTranscript switches the journal to the id a loaded session carries,
// so its record keeps growing in the same file across resumes. A session
// without an id keeps the current journal.
func (cli *ChatCLI) adoptTranscript(id string) {
	if id == "" || (cli.transcript != nil && cli.transcript.id == id) {
		return
	}
	cli.initTranscriptJournal(id)
}
