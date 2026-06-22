/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// CCR — Contextual Compression Retrieval.
//
// When a compressor drops part of a payload, it first writes the *full*
// original to a Store and embeds a retrieval marker in the reduced output.
// The model sees the marker, and if it needs the dropped detail it calls the
// @recall tool, which reads it back verbatim. This is what lets the layer be
// aggressive without ever losing information.
//
// Keys are content-addressed (a short SHA-256 prefix), so storing the same
// content twice is idempotent and natural deduplication falls out for free.

// keyLen is the number of hex characters kept from the SHA-256 digest. 16 hex
// chars = 64 bits of address space — collision-safe for the volume a single
// session produces, while staying short enough to read in a prompt.
const keyLen = 16

// markerPrefix/markerSuffix delimit a CCR retrieval marker embedded in
// compressed output, e.g. "<<ccr:1a2b3c4d5e6f7a8b>>". The form is ASCII-only
// and unlikely to collide with real content.
const (
	markerPrefix = "<<ccr:"
	markerSuffix = ">>"
)

var markerRe = regexp.MustCompile(`<<ccr:([0-9a-f]{` + itoa(keyLen) + `})>>`)

// KeyFor returns the content-addressed CCR key for content. Deterministic and
// collision-resistant for session-scale volumes.
func KeyFor(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:keyLen]
}

// FormatMarker renders the retrieval marker for a key.
func FormatMarker(key string) string {
	return markerPrefix + key + markerSuffix
}

// isValidKey reports whether key is a well-formed CCR key: exactly keyLen
// lowercase hex characters. Used as a hard boundary check before a key is ever
// turned into a filesystem path, so a caller-supplied key (e.g. from an
// @recall tool call) can never escape the store directory via path traversal.
func isValidKey(key string) bool {
	if len(key) != keyLen {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ExtractKeys returns every distinct CCR key referenced in s, in first-seen
// order. Used by the @recall tool to resolve markers and by metrics.
func ExtractKeys(s string) []string {
	matches := markerRe.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		k := m[1]
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

// StoreStats is a point-in-time snapshot of a Store's footprint.
type StoreStats struct {
	Entries    int
	TotalBytes int64
	MaxBytes   int64
}

// Store persists compression originals for on-demand retrieval. Implementations
// must be safe for concurrent use.
type Store interface {
	// Put stores content and returns its content-addressed key. Storing the
	// same content again is idempotent (same key, no duplicate write) and
	// refreshes the entry's recency for eviction purposes.
	Put(content string) (key string, err error)

	// Get returns the original for key. ok is false when the key is unknown
	// or has been evicted.
	Get(key string) (content string, ok bool, err error)

	// Stats reports the current footprint.
	Stats() StoreStats
}

// ─── MemoryStore ────────────────────────────────────────────────────────────

// MemoryStore is an in-process, unbounded Store. Ideal for tests and for the
// one-shot (-p) path where nothing should touch disk. For long-running
// sessions prefer DiskStore, which is bounded.
type MemoryStore struct {
	mu   sync.RWMutex
	data map[string]string
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string]string)}
}

// Put implements Store.
func (m *MemoryStore) Put(content string) (string, error) {
	key := KeyFor(content)
	m.mu.Lock()
	m.data[key] = content
	m.mu.Unlock()
	return key, nil
}

// Get implements Store.
func (m *MemoryStore) Get(key string) (string, bool, error) {
	m.mu.RLock()
	v, ok := m.data[key]
	m.mu.RUnlock()
	return v, ok, nil
}

// Stats implements Store.
func (m *MemoryStore) Stats() StoreStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var total int64
	for _, v := range m.data {
		total += int64(len(v))
	}
	return StoreStats{Entries: len(m.data), TotalBytes: total, MaxBytes: 0}
}

// ─── DiskStore ──────────────────────────────────────────────────────────────

// DiskStore is a bounded, content-addressed, crash-safe on-disk Store.
//
// Each original is written to "<dir>/<key>.ccr" as raw bytes. Because the
// filename *is* the content hash, the store needs no separate index file that
// could be corrupted or drift from reality — the directory is the index. File
// modification time doubles as the last-access timestamp (refreshed on Put and
// Get), which drives both TTL pruning and LRU eviction when the total size
// exceeds the cap.
type DiskStore struct {
	dir      string
	maxBytes int64
	ttl      time.Duration

	mu         sync.Mutex
	entries    map[string]*diskEntry // key -> metadata
	totalBytes int64
}

type diskEntry struct {
	size       int64
	lastAccess time.Time
}

const ccrFileExt = ".ccr"

// NewDiskStore opens (creating if needed) a bounded store rooted at dir. A
// maxBytes <= 0 disables the size cap; a ttl <= 0 disables TTL pruning. On
// open it scans existing entries, prunes any past their TTL, and evicts down
// to the cap so a restart inherits a healthy footprint.
func NewDiskStore(dir string, maxBytes int64, ttl time.Duration) (*DiskStore, error) {
	// #nosec G703 -- dir is the operator-configured CCR store path
	// (CHATCLI_COMPRESSION_CCR_DIR or ~/.chatcli/ccr), not attacker input.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	s := &DiskStore{
		dir:      dir,
		maxBytes: maxBytes,
		ttl:      ttl,
		entries:  make(map[string]*diskEntry),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// load scans the directory and seeds in-memory metadata, then prunes/evicts.
func (s *DiskStore) load() error {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ccrFileExt {
			continue
		}
		key := e.Name()[:len(e.Name())-len(ccrFileExt)]
		if !isValidKey(key) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		s.entries[key] = &diskEntry{size: info.Size(), lastAccess: info.ModTime()}
		s.totalBytes += info.Size()
	}
	s.pruneTTL(now)
	s.evictLocked()
	return nil
}

// path returns the on-disk path for a key.
func (s *DiskStore) path(key string) string {
	return filepath.Join(s.dir, key+ccrFileExt)
}

// Put implements Store. The write is atomic (temp file + rename) so a crash
// never leaves a partial original under a valid content hash.
func (s *DiskStore) Put(content string) (string, error) {
	key := KeyFor(content)
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if e, ok := s.entries[key]; ok {
		// Idempotent: content already stored. Refresh recency only.
		e.lastAccess = now
		_ = os.Chtimes(s.path(key), now, now)
		return key, nil
	}

	final := s.path(key)
	tmp, err := os.CreateTemp(s.dir, key+".*.tmp")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, werr := tmp.WriteString(content); werr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", werr
	}
	if cerr := tmp.Close(); cerr != nil {
		_ = os.Remove(tmpName)
		return "", cerr
	}
	if rerr := os.Rename(tmpName, final); rerr != nil {
		_ = os.Remove(tmpName)
		return "", rerr
	}
	_ = os.Chtimes(final, now, now)

	s.entries[key] = &diskEntry{size: int64(len(content)), lastAccess: now}
	s.totalBytes += int64(len(content))
	s.evictLocked()
	return key, nil
}

// Get implements Store. A hit refreshes the entry's recency.
func (s *DiskStore) Get(key string) (string, bool, error) {
	if !isValidKey(key) {
		// Reject malformed/caller-supplied keys before they touch the
		// filesystem — defends the @recall path against traversal.
		return "", false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[key]
	if !ok {
		return "", false, nil
	}
	// #nosec G304 -- key is validated by isValidKey (fixed-width lowercase hex)
	// and joined under s.dir, so the path cannot escape the store directory.
	data, err := os.ReadFile(s.path(key))
	if err != nil {
		if os.IsNotExist(err) {
			// Drifted: file vanished under us. Forget it cleanly.
			delete(s.entries, key)
			s.totalBytes -= e.size
			return "", false, nil
		}
		return "", false, err
	}
	now := time.Now()
	e.lastAccess = now
	_ = os.Chtimes(s.path(key), now, now)
	return string(data), true, nil
}

// Stats implements Store.
func (s *DiskStore) Stats() StoreStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return StoreStats{Entries: len(s.entries), TotalBytes: s.totalBytes, MaxBytes: s.maxBytes}
}

// pruneTTL removes entries whose last access is older than the TTL. Caller
// must hold the mutex.
func (s *DiskStore) pruneTTL(now time.Time) {
	if s.ttl <= 0 {
		return
	}
	cutoff := now.Add(-s.ttl)
	for key, e := range s.entries {
		if e.lastAccess.Before(cutoff) {
			s.removeLocked(key, e)
		}
	}
}

// evictLocked removes least-recently-used entries until the footprint is
// within the cap. Caller must hold the mutex.
func (s *DiskStore) evictLocked() {
	if s.maxBytes <= 0 || s.totalBytes <= s.maxBytes {
		return
	}
	type kv struct {
		key string
		e   *diskEntry
	}
	all := make([]kv, 0, len(s.entries))
	for k, e := range s.entries {
		all = append(all, kv{k, e})
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].e.lastAccess.Before(all[j].e.lastAccess)
	})
	for _, item := range all {
		if s.totalBytes <= s.maxBytes {
			break
		}
		s.removeLocked(item.key, item.e)
	}
}

// removeLocked deletes one entry from disk and memory. Caller must hold the
// mutex.
func (s *DiskStore) removeLocked(key string, e *diskEntry) {
	_ = os.Remove(s.path(key))
	delete(s.entries, key)
	s.totalBytes -= e.size
}

// itoa is a tiny dependency-free int->string for the regexp builder above
// (avoids importing strconv at package init for a single constant).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
