/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// ErrEntryTooLarge is returned by a bounded Store's Put when the content
// exceeds the per-entry capacity. Storing it would force the eviction pass to
// remove the entry itself (along with everything else), leaving any embedded
// retrieval marker dangling — so the store refuses up front and the caller
// degrades to passthrough, honoring the never-degrade contract.
var ErrEntryTooLarge = errors.New("compress: content exceeds the CCR store per-entry capacity")

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
//
// SCOPE — the store is per OS user (one directory under ~/.chatcli), shared
// by every ChatCLI process that user runs: REPL and gateway daemon alike.
// That sharing is intentional (cross-process recall, content dedup). It also
// means markers persisted in conversation logs are recallable by any process
// reading the same store, so the gateway must remain single-principal per OS
// user; a multi-tenant deployment needs per-principal store directories
// (CHATCLI_COMPRESSION_CCR_DIR).

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
	// Curation visibility: age of the least-recently-accessed entry, how many
	// entries are already past the TTL (i.e. would be removed by a prune), and
	// the configured TTL (0 = disabled). Let the /config surface show that
	// curation is happening rather than leaving the store opaque.
	OldestAge    time.Duration
	StaleEntries int
	TTL          time.Duration
}

// PruneResult reports what a curation pass removed and what remains, so the
// user gets concrete feedback ("freed N entries / X bytes") instead of a
// silent cleanup.
type PruneResult struct {
	Removed          int
	BytesFreed       int64
	RemainingEntries int
	RemainingBytes   int64
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

// Pruner is the optional capability of a Store that curates itself on demand —
// dropping TTL-expired entries and evicting down to the size cap. It is kept
// separate from Store (probed via a type assertion in Layer.Prune) so adding
// curation does not break the Store contract for existing implementations.
type Pruner interface {
	// Prune curates the store now and returns what was removed. Idempotent
	// and safe to call at any time.
	Prune() PruneResult
}

// ─── MemoryStore ────────────────────────────────────────────────────────────

// MemoryStore is an in-process Store. Unbounded by default (tests and the
// one-shot -p path, where sessions are short and nothing should touch disk);
// NewBoundedMemoryStore adds the same LRU size cap and per-entry capacity as
// DiskStore, for use as the long-running fallback when the disk store cannot
// be opened. TTL is deliberately absent: it exists to curate entries across
// restarts, and a memory store never survives one — the LRU cap is what bounds
// a long-lived process.
type MemoryStore struct {
	maxBytes int64 // <= 0 means unbounded

	mu         sync.RWMutex
	data       map[string]*memEntry
	totalBytes int64
}

type memEntry struct {
	content    string
	lastAccess time.Time
}

// NewMemoryStore returns an empty, unbounded in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string]*memEntry)}
}

// NewBoundedMemoryStore returns an in-memory store bounded at maxBytes with
// LRU eviction and a per-entry capacity of maxBytes/4 (see ErrEntryTooLarge).
// A maxBytes <= 0 yields an unbounded store, same as NewMemoryStore.
func NewBoundedMemoryStore(maxBytes int64) *MemoryStore {
	return &MemoryStore{maxBytes: maxBytes, data: make(map[string]*memEntry)}
}

// Put implements Store.
func (m *MemoryStore) Put(content string) (string, error) {
	if limit := maxEntryBytes(m.maxBytes); limit > 0 && int64(len(content)) > limit {
		return "", ErrEntryTooLarge
	}
	key := KeyFor(content)
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.data[key]; ok {
		e.lastAccess = now // idempotent: refresh recency only
		return key, nil
	}
	m.data[key] = &memEntry{content: content, lastAccess: now}
	m.totalBytes += int64(len(content))
	m.evictLocked()
	return key, nil
}

// Get implements Store. A hit refreshes the entry's recency.
func (m *MemoryStore) Get(key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.data[key]
	if !ok {
		return "", false, nil
	}
	e.lastAccess = time.Now()
	return e.content, true, nil
}

// Stats implements Store.
func (m *MemoryStore) Stats() StoreStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return StoreStats{Entries: len(m.data), TotalBytes: m.totalBytes, MaxBytes: m.maxBytes}
}

// Prune implements Pruner: evicts down to the size cap (a no-op when
// unbounded, since Put keeps a bounded store within its cap continuously).
func (m *MemoryStore) Prune() PruneResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	beforeN, beforeBytes := len(m.data), m.totalBytes
	m.evictLocked()
	return PruneResult{
		Removed:          beforeN - len(m.data),
		BytesFreed:       beforeBytes - m.totalBytes,
		RemainingEntries: len(m.data),
		RemainingBytes:   m.totalBytes,
	}
}

// evictLocked removes least-recently-used entries until the footprint is
// within the cap. Caller must hold the mutex. No-op when unbounded.
func (m *MemoryStore) evictLocked() {
	if m.maxBytes <= 0 || m.totalBytes <= m.maxBytes {
		return
	}
	type kv struct {
		key string
		e   *memEntry
	}
	all := make([]kv, 0, len(m.data))
	for k, e := range m.data {
		all = append(all, kv{k, e})
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].e.lastAccess.Before(all[j].e.lastAccess)
	})
	for _, item := range all {
		if m.totalBytes <= m.maxBytes {
			break
		}
		delete(m.data, item.key)
		m.totalBytes -= int64(len(item.e.content))
	}
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
	entries    map[string]*diskEntry // key -> metadata (cache of the directory)
	totalBytes int64
	lastSweep  time.Time // throttles the on-Put sweep (see maybeSweepLocked)
}

type diskEntry struct {
	size       int64
	lastAccess time.Time
}

const ccrFileExt = ".ccr"

// perEntryCapDivisor bounds a single entry to maxBytes/perEntryCapDivisor.
// The invariant it buys: after evicting older entries, a just-put entry always
// fits within the cap, so the eviction pass can never remove the entry it was
// triggered by — the failure mode where Put returned a key whose content was
// already gone (a dangling <<ccr:KEY>> marker). The divisor also keeps one
// pathological payload from monopolizing the store: at least 4 originals of
// maximal size coexist.
const perEntryCapDivisor = 4

// maxEntryBytes returns the per-entry capacity for a store bounded at
// maxBytes, or 0 when the store is unbounded (no per-entry limit).
func maxEntryBytes(maxBytes int64) int64 {
	if maxBytes <= 0 {
		return 0
	}
	return maxBytes / perEntryCapDivisor
}

// ccrSweepInterval bounds how often a curation sweep (directory rescan + TTL
// prune) runs mid-session. Sweeps are otherwise only done at startup (load);
// without this, a long-lived process (e.g. the gateway daemon) would never
// expire stale entries — nor notice entries added/evicted by the other
// ChatCLI process sharing the directory — until a restart. Hourly keeps the
// O(n) directory scan off the hot path while still curating during active use.
const ccrSweepInterval = time.Hour

// NewDiskStore opens (creating if needed) a bounded store rooted at dir. A
// maxBytes <= 0 disables the size cap; a ttl <= 0 disables TTL pruning. On
// open it scans existing entries, prunes any past their TTL, and evicts down
// to the cap so a restart inherits a healthy footprint.
//
// A bounded store also enforces a per-entry capacity (maxBytes/4): Put returns
// ErrEntryTooLarge for content that would immediately fall out of the cap,
// instead of accepting it and letting eviction leave the returned key dangling.
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
	if err := s.scanLocked(); err != nil {
		return err
	}
	now := time.Now()
	s.pruneTTL(now)
	s.evictLocked()
	s.lastSweep = now
	return nil
}

// scanLocked rebuilds the in-memory index from the directory — the source of
// truth shared with any other ChatCLI process using the same CCR dir. File
// mtime doubles as last-access across processes (both Put and Get refresh it
// via Chtimes), so rebuilding from mtime never loses recency information.
// Caller must hold the mutex.
func (s *DiskStore) scanLocked() error {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	entries := make(map[string]*diskEntry, len(ents))
	var total int64
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
		entries[key] = &diskEntry{size: info.Size(), lastAccess: info.ModTime()}
		total += info.Size()
	}
	s.entries = entries
	s.totalBytes = total
	return nil
}

// path returns the on-disk path for a key.
func (s *DiskStore) path(key string) string {
	return filepath.Join(s.dir, key+ccrFileExt)
}

// Put implements Store. The write is atomic (temp file + rename) so a crash
// never leaves a partial original under a valid content hash.
func (s *DiskStore) Put(content string) (string, error) {
	if limit := maxEntryBytes(s.maxBytes); limit > 0 && int64(len(content)) > limit {
		return "", ErrEntryTooLarge
	}
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
	s.maybeSweepLocked(now)
	s.evictLocked()
	return key, nil
}

// maybeSweepLocked runs a curation sweep — directory rescan (reconciling with
// entries added/removed by other processes) plus TTL prune — at most once per
// ccrSweepInterval, so a long-running session stays curated and honest about
// its real footprint, not just at startup. A rescan failure keeps the current
// view: the next Get/Put self-heals per key, and the next sweep retries.
// Caller must hold the mutex.
func (s *DiskStore) maybeSweepLocked(now time.Time) {
	if !s.lastSweep.IsZero() && now.Sub(s.lastSweep) < ccrSweepInterval {
		return
	}
	_ = s.scanLocked()
	s.pruneTTL(now)
	s.lastSweep = now
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
		// Not in this process's index — but another ChatCLI process sharing
		// the directory (REPL vs gateway daemon) may have offloaded it after
		// our last scan. The directory is the source of truth: fall through to
		// disk and adopt the entry on a hit, so cross-process @recall works
		// immediately instead of waiting for the next sweep. The key is
		// already validated, so this touches only paths inside the store.
		return s.adoptFromDiskLocked(key)
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

// adoptFromDiskLocked resolves a Get miss against the directory itself and,
// on a hit, adopts the entry into the in-memory index (refreshing recency so
// the adopted entry is not the next eviction victim). Caller must hold the
// mutex and have validated the key.
func (s *DiskStore) adoptFromDiskLocked(key string) (string, bool, error) {
	// #nosec G304 -- key is validated by isValidKey (fixed-width lowercase hex)
	// and joined under s.dir, so the path cannot escape the store directory.
	data, err := os.ReadFile(s.path(key))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil // genuinely unknown key
		}
		return "", false, err
	}
	now := time.Now()
	s.entries[key] = &diskEntry{size: int64(len(data)), lastAccess: now}
	s.totalBytes += int64(len(data))
	_ = os.Chtimes(s.path(key), now, now)
	return string(data), true, nil
}

// Stats implements Store. Beyond the raw footprint it computes the
// least-recently-accessed entry's age and how many entries are already past
// the TTL, so the /config surface can show curation status.
func (s *DiskStore) Stats() StoreStats {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var oldest time.Time
	stale := 0
	cutoff := now.Add(-s.ttl)
	for _, e := range s.entries {
		if oldest.IsZero() || e.lastAccess.Before(oldest) {
			oldest = e.lastAccess
		}
		if s.ttl > 0 && e.lastAccess.Before(cutoff) {
			stale++
		}
	}
	var oldestAge time.Duration
	if !oldest.IsZero() {
		oldestAge = now.Sub(oldest)
	}
	return StoreStats{
		Entries:      len(s.entries),
		TotalBytes:   s.totalBytes,
		MaxBytes:     s.maxBytes,
		OldestAge:    oldestAge,
		StaleEntries: stale,
		TTL:          s.ttl,
	}
}

// Prune implements Store: a directory rescan (reconciling with other
// processes), TTL prune, and size-cap eviction, run on demand. The
// before/after delta is computed over the reconciled view so the report
// reflects what this pass actually freed, not stale accounting.
func (s *DiskStore) Prune() PruneResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	_ = s.scanLocked()
	beforeN, beforeBytes := len(s.entries), s.totalBytes
	now := time.Now()
	s.pruneTTL(now)
	s.evictLocked()
	s.lastSweep = now
	return PruneResult{
		Removed:          beforeN - len(s.entries),
		BytesFreed:       beforeBytes - s.totalBytes,
		RemainingEntries: len(s.entries),
		RemainingBytes:   s.totalBytes,
	}
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
