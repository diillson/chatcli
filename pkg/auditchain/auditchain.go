/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Hash-chained, multi-writer audit trail.
 *
 * Every line is a JSON object carrying seq, prev_hash, chain_v and hash,
 * where hash = SHA-256(prev_hash ‖ 0x00 ‖ canonical JSON of the object
 * without hash). Canonical means encoding/json's map encoding (sorted
 * keys), so any writer — the REPL, a gateway, the gRPC server — chains
 * the same way regardless of the Go struct it started from.
 *
 * Writers from different processes share one file: each append takes an
 * exclusive file lock, re-reads the tail when the file changed under it
 * (another writer appended, or the file rotated) and only then links the
 * new line. Files rotate at MaxBytes; the first line of the new file
 * names the file it continues (rotated_from) and links to its last hash,
 * so Verify follows the chain across the rotation boundary. With
 * encryption at rest enabled, every line is sealed before it hits disk.
 *
 * A torn last line (a crash mid-write) is reported, never mistaken for
 * tampering: the reader skips it and the next append continues from the
 * last complete line.
 */
package auditchain

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/pkg/atrest"
)

const (
	// ChainVersion is written on every line as chain_v.
	ChainVersion = 2
	// DefaultMaxBytes is the rotation threshold.
	DefaultMaxBytes int64 = 64 << 20
	// sealedPrefix marks an encrypted line.
	sealedPrefix = "enc:"
	// maxLine bounds one entry.
	maxLine = 16 << 20
)

// Chain field names.
const (
	fieldSeq         = "seq"
	fieldPrev        = "prev_hash"
	fieldHash        = "hash"
	fieldVersion     = "chain_v"
	fieldRotatedFrom = "rotated_from"
)

// Options tunes a Writer.
type Options struct {
	// MaxBytes rotates the file when it reaches this size (0 = default,
	// negative = never).
	MaxBytes int64
	// Seal encrypts each line with the at-rest key; nil means "when the
	// key is configured".
	Seal func() bool
}

// Writer appends chained entries to a file shared with other writers.
type Writer struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	seq      int64
	last     string
	size     int64
	maxBytes int64
	seal     func() bool
}

// Open opens (or creates) the trail at path and picks up its chain tail.
func Open(path string, opts Options) (*Writer, error) {
	f, err := openAppend(path)
	if err != nil {
		return nil, err
	}
	w := &Writer{path: path, f: f, maxBytes: opts.MaxBytes, seal: opts.Seal}
	if w.maxBytes == 0 {
		w.maxBytes = DefaultMaxBytes
	}
	if w.seal == nil {
		w.seal = atrest.Enabled
	}
	w.refreshTail()
	return w, nil
}

func openAppend(path string) (*os.File, error) {
	return os.OpenFile(filepath.Clean(path), os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 G703 -- operator-configured absolute audit path, cleaned; callers validate
}

// refreshTail re-reads seq/hash/size from the file (caller holds w.mu).
func (w *Writer) refreshTail() {
	st, err := w.f.Stat()
	if err != nil {
		return
	}
	w.size = st.Size()
	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return
	}
	w.seq, w.last, _ = tailOf(w.f)
}

// Path returns the trail path.
func (w *Writer) Path() string { return w.path }

// Append chains v (any JSON object) onto the trail. The chain fields in v,
// if any, are replaced.
func (w *Writer) Append(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("audit entry must be a JSON object: %w", err)
	}
	for _, k := range []string{fieldSeq, fieldPrev, fieldHash, fieldVersion, fieldRotatedFrom} {
		delete(m, k)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := lockFile(w.f); err != nil {
		return err
	}
	defer func() { _ = unlockFile(w.f) }()
	if err := w.syncWithDisk(); err != nil {
		return err
	}
	if w.maxBytes > 0 && w.size >= w.maxBytes {
		if from, err := w.rotate(); err == nil && from != "" {
			m[fieldRotatedFrom] = mustRaw(from)
		}
	}
	line, hash, err := encodeEntry(m, w.seq+1, w.last)
	if err != nil {
		return err
	}
	if w.seal() {
		sealed, err := atrest.Seal(line)
		if err != nil {
			return err
		}
		line = []byte(sealedPrefix + base64.StdEncoding.EncodeToString(sealed))
	}
	line = append(line, '\n')
	if _, err := w.f.Write(line); err != nil {
		return err
	}
	w.seq++
	w.last = hash
	w.size += int64(len(line))
	return nil
}

// syncWithDisk notices another writer's appends or a rotation (caller
// holds w.mu and the file lock).
func (w *Writer) syncWithDisk() error {
	onDisk, err := os.Stat(w.path)
	if errors.Is(err, os.ErrNotExist) {
		// Rotated (or removed) by another writer: reopen the live path.
		return w.reopen()
	}
	if err != nil {
		return err
	}
	cur, err := w.f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(onDisk, cur) {
		return w.reopen()
	}
	if cur.Size() != w.size {
		w.refreshTail()
	}
	// A torn last line (crash mid-write; under the lock nobody else is
	// mid-write) is terminated so the next entry starts on its own line.
	if w.size > 0 {
		var b [1]byte
		if _, err := w.f.ReadAt(b[:], w.size-1); err == nil && b[0] != '\n' {
			if _, err := w.f.Write([]byte{'\n'}); err != nil {
				return err
			}
			w.size++
		}
	}
	return nil
}

func (w *Writer) reopen() error {
	nf, err := openAppend(w.path)
	if err != nil {
		return err
	}
	_ = unlockFile(w.f)
	_ = w.f.Close()
	w.f = nf
	if err := lockFile(w.f); err != nil {
		return err
	}
	w.refreshTail()
	return nil
}

// rotate renames the current file aside and opens a fresh one; returns
// the rotated file's base name (caller holds w.mu and the lock).
func (w *Writer) rotate() (string, error) {
	aside := w.path + "." + time.Now().UTC().Format("20060102T150405.000000000Z")
	if err := os.Rename(w.path, aside); err != nil {
		return "", err
	}
	nf, err := openAppend(w.path)
	if err != nil {
		return "", err
	}
	_ = unlockFile(w.f)
	_ = w.f.Close()
	w.f = nf
	if err := lockFile(w.f); err != nil {
		return "", err
	}
	// seq and last carry over: the new file continues the chain.
	w.size = 0
	return filepath.Base(aside), nil
}

// Close releases the file.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

func mustRaw(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// encodeEntry adds the chain fields and returns the line and its hash.
func encodeEntry(m map[string]json.RawMessage, seq int64, prev string) ([]byte, string, error) {
	m[fieldSeq] = mustRaw64(seq)
	m[fieldVersion] = mustRaw64(ChainVersion)
	if prev != "" {
		m[fieldPrev] = mustRaw(prev)
	}
	hash, err := hashOf(m, prev)
	if err != nil {
		return nil, "", err
	}
	m[fieldHash] = mustRaw(hash)
	line, err := json.Marshal(m)
	if err != nil {
		return nil, "", err
	}
	return line, hash, nil
}

func mustRaw64(n int64) json.RawMessage { return json.RawMessage(fmt.Sprintf("%d", n)) }

// hashOf computes SHA-256(prev ‖ 0 ‖ canonical(m without hash)).
func hashOf(m map[string]json.RawMessage, prev string) (string, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k == fieldHash {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var canonical bytes.Buffer
	canonical.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			canonical.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		canonical.Write(kb)
		canonical.WriteByte(':')
		var compact bytes.Buffer
		if err := json.Compact(&compact, m[k]); err != nil {
			return "", err
		}
		canonical.Write(compact.Bytes())
	}
	canonical.WriteByte('}')
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write([]byte{0})
	h.Write(canonical.Bytes())
	return hex.EncodeToString(h.Sum(nil)), nil
}

// decodeLine opens a sealed line; plain lines pass through.
func decodeLine(line []byte) ([]byte, error) {
	if !bytes.HasPrefix(line, []byte(sealedPrefix)) {
		return line, nil
	}
	if !atrest.Enabled() {
		return nil, errors.New("sealed entry and no encryption key configured")
	}
	sealed, err := base64.StdEncoding.DecodeString(string(line[len(sealedPrefix):]))
	if err != nil {
		return nil, err
	}
	return atrest.Open(sealed)
}

// lineReader yields lines and whether each was newline-terminated.
type lineReader struct {
	r *bufio.Reader
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{r: bufio.NewReaderSize(r, 256*1024)}
}

// next returns the next line (without the newline), whether it was
// terminated, and io.EOF when done.
func (lr *lineReader) next() ([]byte, bool, error) {
	line, err := lr.r.ReadBytes('\n')
	if len(line) > maxLine {
		return nil, false, errors.New("audit line exceeds the size limit")
	}
	if err == nil {
		return bytes.TrimRight(line, "\r\n"), true, nil
	}
	if errors.Is(err, io.EOF) {
		if len(line) == 0 {
			return nil, false, io.EOF
		}
		return line, false, nil
	}
	return nil, false, err
}

// tailOf returns the last chained seq/hash in r, skipping a torn last line.
func tailOf(r io.Reader) (int64, string, bool) {
	lr := newLineReader(r)
	var seq int64
	var last string
	torn := false
	for {
		line, terminated, err := lr.next()
		if err != nil {
			break
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		plain, err := decodeLine(line)
		if err != nil {
			if !terminated {
				torn = true
			}
			continue
		}
		var e struct {
			Seq  int64  `json:"seq"`
			Hash string `json:"hash"`
		}
		if err := json.Unmarshal(plain, &e); err != nil || e.Hash == "" {
			if !terminated {
				torn = true
			}
			continue
		}
		seq, last = e.Seq, e.Hash
	}
	return seq, last, torn
}

// Tail returns the last chained seq/hash in the trail at path (0, "" for
// a new or legacy file).
func Tail(path string) (int64, string) {
	f, err := os.Open(filepath.Clean(path)) // #nosec G304 G703 -- operator-configured audit path, cleaned; callers validate
	if err != nil {
		return 0, ""
	}
	defer func() { _ = f.Close() }()
	seq, last, _ := tailOf(f)
	return seq, last
}

// LegacyVerifier checks one pre-chain_v line: given the raw JSON and the
// expected previous hash it returns the line's hash and whether the line
// verified. nil treats such lines as legacy (counted, not verified).
type LegacyVerifier func(line []byte, prev string) (hash string, ok bool)

// Report is the outcome of Verify.
type Report struct {
	Entries     int
	Chained     int    // entries carrying a hash
	Legacy      int    // entries written before the chain existed
	Sealed      int    // entries stored encrypted
	BrokenAt    int    // 1-based line of the first break (0 = intact)
	Err         string // what broke
	Torn        bool   // the last line is incomplete (crash mid-write), not tampering
	RotatedFrom string // the file this one continues, when it starts mid-chain
}

// Intact reports whether every chained entry verified.
func (r Report) Intact() bool { return r.BrokenAt == 0 }

// Verify re-hashes the trail at path and reports the first line whose hash
// or previous-hash link does not match. legacy verifies version-1 lines.
func Verify(path string, legacy LegacyVerifier) (Report, error) {
	var rep Report
	f, err := os.Open(filepath.Clean(path)) // #nosec G304 G703 -- operator-supplied audit path, cleaned; callers validate
	if err != nil {
		return rep, err
	}
	defer func() { _ = f.Close() }()
	return verifyReader(f, legacy)
}

func verifyReader(r io.Reader, legacy LegacyVerifier) (Report, error) {
	var rep Report
	lr := newLineReader(r)
	prev := ""
	line := 0
	// tornAt is a terminated but unparseable line waiting to see whether
	// the chain resumes right after it (crash residue) or not (damage).
	tornAt := 0
	for {
		raw, terminated, err := lr.next()
		if errors.Is(err, io.EOF) {
			if tornAt > 0 {
				rep.Torn = true
			}
			return rep, nil
		}
		if err != nil {
			return rep, err
		}
		line++
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		rep.Entries++
		plain, err := decodeLine(raw)
		if err != nil {
			if !terminated {
				rep.Torn = true
				rep.Entries--
				return rep, nil
			}
			rep.BrokenAt, rep.Err = line, err.Error()
			return rep, nil
		}
		if bytes.HasPrefix(raw, []byte(sealedPrefix)) {
			rep.Sealed++
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(plain, &m); err != nil {
			rep.Entries--
			if !terminated {
				rep.Torn = true
				return rep, nil
			}
			if tornAt > 0 {
				rep.BrokenAt, rep.Err = tornAt, "unparseable entry"
				return rep, nil
			}
			tornAt = line
			continue
		}
		hash := rawString(m[fieldHash])
		if hash == "" {
			rep.Legacy++
			continue
		}
		if tornAt > 0 {
			// The chain must resume exactly where it left off for the
			// unparseable line to count as crash residue.
			if rawString(m[fieldPrev]) != prev {
				rep.BrokenAt, rep.Err = tornAt, "unparseable entry"
				return rep, nil
			}
			rep.Torn, tornAt = true, 0
		}
		if _, v2 := m[fieldVersion]; !v2 {
			// Version-1 line: struct-order canonical form, verified by the
			// owner of that struct.
			if legacy == nil {
				// Counted, not verified; the chain still continues from it.
				rep.Legacy++
				prev = hash
				continue
			}
			rep.Chained++
			got, ok := legacy(plain, prev)
			if !ok {
				rep.BrokenAt, rep.Err = line, "entry hash mismatch"
				return rep, nil
			}
			prev = got
			continue
		}
		rep.Chained++
		linked := rawString(m[fieldPrev])
		if linked != prev {
			from := rawString(m[fieldRotatedFrom])
			if rep.Chained == 1 && from != "" {
				rep.RotatedFrom = from
				prev = linked
			} else {
				rep.BrokenAt, rep.Err = line, "previous-hash link mismatch"
				return rep, nil
			}
		}
		want, err := hashOf(m, prev)
		if err != nil || want != hash {
			rep.BrokenAt, rep.Err = line, "entry hash mismatch"
			return rep, nil
		}
		prev = hash
	}
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// Rotated lists the rotated files of the trail at path, oldest first.
func Rotated(path string) []string {
	matches, err := filepath.Glob(path + ".*")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(matches))
	base := filepath.Base(path) + "."
	for _, m := range matches {
		name := filepath.Base(m)
		if !strings.HasPrefix(name, base) || !strings.HasSuffix(name, "Z") {
			continue
		}
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// PruneRotated removes rotated files modified before cutoff and returns
// how many went.
func PruneRotated(path string, cutoff time.Time) int {
	n := 0
	for _, p := range Rotated(path) {
		st, err := os.Stat(p)
		if err != nil || !st.ModTime().Before(cutoff) {
			continue
		}
		if os.Remove(p) == nil {
			n++
		}
	}
	return n
}
