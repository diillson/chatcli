/*
 * ChatCLI - Long-term memory: explicit note links (links.json)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The knowledge graph is derived from the stores, so every link it holds is
 * re-derivable — except the ones a person (or the model, on their behalf)
 * draws on purpose: "this fact is about that project", "this doc explains
 * that gotcha". Those are durable user data and live here, one record per
 * (from, to, rel), feeding the graph builder as OriginUser links.
 *
 * Endpoints are graph node ids ("fact:<id>", "topic:<slug>", "doc:<ctx>:<path>",
 * …). Fact ids are content hashes, so a link survives export/import; a
 * superseded fact is followed to its successor by the builder (provenance
 * chain), so a link survives supersession too.
 *
 * Multi-process discipline matches the other stores (flock + merge from disk
 * + atomic sealed write), with one addition the additive stores do not need:
 * removals are tombstones (Deleted + UpdatedAt) merged last-writer-wins, so a
 * link removed in one process is not resurrected by another process's merge.
 */
package memory

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/pkg/flock"
	"github.com/diillson/chatcli/pkg/textfold"
	"go.uber.org/zap"
)

const (
	// maxStoredLinks bounds links.json; a link is ~200 bytes, so the cap
	// keeps the file around 2MB at worst.
	maxStoredLinks = 10_000
	// maxLinkEndpointBytes bounds a node id (doc ids carry a source path).
	maxLinkEndpointBytes = 512
	// maxLinkNoteBytes bounds the optional free-text note on a link.
	maxLinkNoteBytes = 280
	// maxLinkRelBytes bounds a custom relation name.
	maxLinkRelBytes = 40
	// linkTombstoneTTL is how long a removal is remembered for merges.
	linkTombstoneTTL = 30 * 24 * time.Hour
	// linkDanglingGrace is how long a link may point at a missing note before
	// garbage collection drops it (a context re-ingested, a session restored
	// from backup, a fact re-learned).
	linkDanglingGrace = 30 * 24 * time.Hour
)

// Link store refusals, so callers can say why nothing was saved.
var (
	// ErrLinkInvalid: blank or oversized endpoint, or a self-link.
	ErrLinkInvalid = errors.New("invalid link endpoints")
	// ErrLinksFull: the store holds maxStoredLinks live links.
	ErrLinksFull = errors.New("link store is full")
	// ErrLinksReadOnly: links.json is sealed with a key this process lacks.
	ErrLinksReadOnly = errors.New("link store is read-only (sealed without the key)")
)

// StoredLink is one explicit, durable link between two notes.
type StoredLink struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Rel       string    `json:"rel"`
	Origin    string    `json:"origin,omitempty"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Deleted marks a tombstone: the link was removed at UpdatedAt.
	Deleted bool `json:"deleted,omitempty"`
	// MissingSince is when garbage collection first saw an endpoint absent
	// from the graph (zero = both endpoints exist).
	MissingSince time.Time `json:"missing_since,omitempty"`
}

type storedLinkKey struct{ from, to, rel string }

func (l *StoredLink) key() storedLinkKey { return storedLinkKey{l.From, l.To, l.Rel} }

// LinkStore persists explicit links in <memoryDir>/links.json.
type LinkStore struct {
	latch  storeLatch // read-only once a sealed file could not be opened
	mu     sync.RWMutex
	links  map[storedLinkKey]*StoredLink
	path   string
	logger *zap.Logger
	// diskStamp is links.json's size+mtime as last merged, so reads pick
	// up links other processes wrote without re-reading an unchanged file.
	diskStamp string

	changeNotifier
}

// NewLinkStore loads links.json from memoryDir (missing file = empty store).
func NewLinkStore(memoryDir string, logger *zap.Logger) *LinkStore {
	if logger == nil {
		logger = zap.NewNop()
	}
	ls := &LinkStore{
		links:  make(map[storedLinkKey]*StoredLink),
		path:   memoryDir + "/links.json",
		logger: logger,
	}
	ls.load()
	return ls
}

// NormalizeRel turns a free-form relation into the stored slug: lowercase,
// accents folded, separators collapsed to '_', bounded. Empty means
// "related".
func NormalizeRel(rel string) string {
	rel = strings.ToLower(textfold.Fold(strings.TrimSpace(rel)))
	var b strings.Builder
	under := false
	for _, r := range rel {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if under && b.Len() > 0 {
				b.WriteByte('_')
			}
			under = false
			b.WriteRune(r)
		default:
			under = true
		}
		if b.Len() >= maxLinkRelBytes {
			break
		}
	}
	if b.Len() == 0 {
		return "related"
	}
	return b.String()
}

// Add records (or revives) the link from → to with rel. changed is true
// when the store changed; false with a nil error means the identical link
// already existed. Blank or oversized endpoints and self-links are
// rejected (ErrLinkInvalid); the note is sanitized like fact content.
func (ls *LinkStore) Add(from, to, rel, origin, note string) (changed bool, err error) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" || to == "" || from == to ||
		len(from) > maxLinkEndpointBytes || len(to) > maxLinkEndpointBytes {
		return false, ErrLinkInvalid
	}
	rel = NormalizeRel(rel)
	note = SanitizeFactContent(note)
	if len(note) > maxLinkNoteBytes {
		note = strings.TrimSpace(textfold.Cut(note, maxLinkNoteBytes)) + "…"
	}
	if origin == "" {
		origin = "user"
	}

	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.latch.locked() {
		return false, ErrLinksReadOnly
	}
	ls.mergeFromDiskLocked() // see links other processes added (cap, revive)
	now := time.Now()
	k := storedLinkKey{from, to, rel}
	if cur, ok := ls.links[k]; ok {
		if !cur.Deleted && cur.Note == note && cur.Origin == origin && cur.MissingSince.IsZero() {
			return false, nil
		}
		cur.Deleted = false
		cur.Note = note
		cur.Origin = origin
		cur.UpdatedAt = now
		cur.MissingSince = time.Time{}
	} else {
		if ls.liveCountLocked() >= maxStoredLinks {
			ls.logger.Warn("memory links: store full, link not added", zap.Int("cap", maxStoredLinks))
			return false, ErrLinksFull
		}
		ls.links[k] = &StoredLink{From: from, To: to, Rel: rel, Origin: origin, Note: note, CreatedAt: now, UpdatedAt: now}
	}
	ls.notifyChanged()
	ls.persistLocked()
	return true, nil
}

// Remap moves every live link endpoint found in remap (old node id → new
// node id) to its new id, keeping relation, origin, note and creation
// time; the old records become tombstones. Used when memory rewrites a
// note's identity (LLM compaction rephrasing a fact). Returns how many
// links moved.
func (ls *LinkStore) Remap(remap map[string]string) int {
	if len(remap) == 0 {
		return 0
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.latch.locked() {
		return 0
	}
	ls.mergeFromDiskLocked()
	now := time.Now()
	var moved []StoredLink
	for _, l := range ls.links {
		if l.Deleted {
			continue
		}
		from, to := l.From, l.To
		if n, ok := remap[from]; ok {
			from = n
		}
		if n, ok := remap[to]; ok {
			to = n
		}
		if from == l.From && to == l.To {
			continue
		}
		l.Deleted = true
		l.UpdatedAt = now
		if from == to {
			continue // both ends merged into one note: nothing left to link
		}
		nl := *l
		nl.From, nl.To, nl.Deleted, nl.UpdatedAt, nl.MissingSince = from, to, false, now, time.Time{}
		moved = append(moved, nl)
	}
	for _, nl := range moved {
		k := nl.key()
		if cur, ok := ls.links[k]; ok && !cur.Deleted {
			continue // the new endpoint pair already carries this link
		}
		cp := nl
		ls.links[k] = &cp
	}
	if len(moved) > 0 {
		ls.notifyChanged()
		ls.persistLocked()
	}
	return len(moved)
}

// Remove tombstones the links between a and b — in both directions, and
// only rel when rel is non-empty. Returns how many were removed.
func (ls *LinkStore) Remove(a, b, rel string) int {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if rel != "" {
		rel = NormalizeRel(rel)
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.mergeFromDiskLocked() // a link another process added is removable too
	now := time.Now()
	n := 0
	for _, l := range ls.links {
		if l.Deleted {
			continue
		}
		pair := (l.From == a && l.To == b) || (l.From == b && l.To == a)
		if !pair || (rel != "" && l.Rel != rel) {
			continue
		}
		l.Deleted = true
		l.UpdatedAt = now
		n++
	}
	if n > 0 {
		ls.notifyChanged()
		ls.persistLocked()
	}
	return n
}

// All returns the live links sorted by (from, to, rel), including links
// other processes wrote since this one last read links.json.
func (ls *LinkStore) All() []StoredLink {
	ls.refreshFromDisk()
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	out := make([]StoredLink, 0, len(ls.links))
	for _, l := range ls.links {
		if !l.Deleted {
			out = append(out, *l)
		}
	}
	sortStoredLinks(out)
	return out
}

// Count is the number of live links.
func (ls *LinkStore) Count() int {
	ls.refreshFromDisk()
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return ls.liveCountLocked()
}

// Merge adopts an exported link (import path). Same LWW rule as the
// multi-process merge; returns true when the store changed.
func (ls *LinkStore) Merge(l StoredLink) bool {
	l.From, l.To = strings.TrimSpace(l.From), strings.TrimSpace(l.To)
	if l.From == "" || l.To == "" || l.From == l.To ||
		len(l.From) > maxLinkEndpointBytes || len(l.To) > maxLinkEndpointBytes {
		return false
	}
	l.Rel = NormalizeRel(l.Rel)
	l.Note = SanitizeFactContent(l.Note)
	if len(l.Note) > maxLinkNoteBytes {
		l.Note = strings.TrimSpace(textfold.Cut(l.Note, maxLinkNoteBytes)) + "…"
	}
	if l.Origin == "" {
		l.Origin = "user"
	}
	if l.UpdatedAt.IsZero() {
		l.UpdatedAt = time.Now()
	}
	if l.CreatedAt.IsZero() {
		l.CreatedAt = l.UpdatedAt
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if !ls.adoptLocked(l) {
		return false
	}
	ls.notifyChanged()
	ls.persistLocked()
	return true
}

// GC maintains the store against the current graph: tombstones older than
// their TTL are dropped, and a live link whose endpoint has been missing
// from the graph for longer than the grace period is dropped too. exists
// reports whether a node id is present. Returns how many records were
// dropped.
func (ls *LinkStore) GC(exists func(id string) bool) int {
	if exists == nil {
		return 0
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.latch.locked() {
		return 0
	}
	ls.mergeFromDiskLocked()
	now := time.Now()
	dropped, touched := 0, false
	for k, l := range ls.links {
		if l.Deleted {
			if now.Sub(l.UpdatedAt) > linkTombstoneTTL {
				delete(ls.links, k)
				dropped++
			}
			continue
		}
		if exists(l.From) && exists(l.To) {
			if !l.MissingSince.IsZero() {
				l.MissingSince = time.Time{}
				touched = true
			}
			continue
		}
		if l.MissingSince.IsZero() {
			l.MissingSince = now
			touched = true
			continue
		}
		if now.Sub(l.MissingSince) > linkDanglingGrace {
			// A tombstone, not a delete: another process holding the
			// link in memory must not re-add it on its next merge.
			l.Deleted = true
			l.UpdatedAt = now
			dropped++
		}
	}
	if dropped > 0 || touched {
		ls.persistLocked()
	}
	if dropped > 0 {
		ls.notifyChanged()
	}
	return dropped
}

func (ls *LinkStore) liveCountLocked() int {
	n := 0
	for _, l := range ls.links {
		if !l.Deleted {
			n++
		}
	}
	return n
}

// adoptLocked merges one record last-writer-wins by UpdatedAt.
func (ls *LinkStore) adoptLocked(l StoredLink) bool {
	if l.Deleted && time.Since(l.UpdatedAt) > linkTombstoneTTL {
		// An expired tombstone has done its job; adopting it back from
		// disk would undo GC forever.
		return false
	}
	k := l.key()
	cur, ok := ls.links[k]
	if !ok {
		if !l.Deleted && ls.liveCountLocked() >= maxStoredLinks {
			return false
		}
		cp := l
		ls.links[k] = &cp
		return true
	}
	if !l.UpdatedAt.After(cur.UpdatedAt) {
		return false
	}
	created := cur.CreatedAt
	*cur = l
	if !created.IsZero() && (cur.CreatedAt.IsZero() || created.Before(cur.CreatedAt)) {
		cur.CreatedAt = created
	}
	return true
}

func sortStoredLinks(out []StoredLink) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].Rel < out[j].Rel
	})
}

func (ls *LinkStore) load() {
	ls.diskStamp = fileStamp(ls.path)
	data, err := readStoreFile(ls.path)
	if ls.latch.lockIfSealed(err, ls.logger, "links") || err != nil {
		return
	}
	var links []StoredLink
	if err := json.Unmarshal(data, &links); err != nil {
		if qpath, qerr := quarantineCorrupt(ls.path); qerr == nil {
			ls.logger.Warn("links store corrupt; quarantined for recovery",
				zap.String("quarantine", qpath), zap.Error(err))
		} else {
			ls.logger.Warn("links store corrupt and quarantine failed", zap.Error(qerr))
		}
		return
	}
	for i := range links {
		l := links[i]
		if l.From == "" || l.To == "" {
			continue
		}
		ls.adoptLocked(l)
	}
}

// refreshFromDisk merges links.json when its size or mtime moved since the
// last merge — one stat per read when nothing changed.
func (ls *LinkStore) refreshFromDisk() {
	stamp := fileStamp(ls.path)
	ls.mu.RLock()
	same := stamp == ls.diskStamp
	ls.mu.RUnlock()
	if same {
		return
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if stamp != ls.diskStamp {
		ls.mergeFromDiskLocked()
	}
}

// fileStamp is a cheap change marker for a store file ("" when absent).
func fileStamp(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fi.ModTime().UTC().Format(time.RFC3339Nano) + "|" + strconvItoa(fi.Size())
}

func strconvItoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

func (ls *LinkStore) mergeFromDiskLocked() {
	ls.diskStamp = fileStamp(ls.path)
	data, err := readStoreFile(ls.path)
	if ls.latch.lockIfSealed(err, ls.logger, "links") || err != nil {
		return
	}
	var onDisk []StoredLink
	if err := json.Unmarshal(data, &onDisk); err != nil {
		return
	}
	for i := range onDisk {
		if onDisk[i].From == "" || onDisk[i].To == "" {
			continue
		}
		ls.adoptLocked(onDisk[i])
	}
}

// persistLocked merges the on-disk copy and writes atomically. Caller holds
// ls.mu.
func (ls *LinkStore) persistLocked() {
	unlock := flock.Lock(ls.path)
	defer unlock()
	if ls.latch.locked() {
		return // sealed file we cannot read: never overwrite it
	}
	ls.mergeFromDiskLocked()
	out := make([]StoredLink, 0, len(ls.links))
	for _, l := range ls.links {
		out = append(out, *l)
	}
	sortStoredLinks(out)
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return
	}
	if err := atomicWriteFile(ls.path, data, 0o600); err != nil {
		ls.logger.Warn("failed to write links store", zap.Error(err))
		return
	}
	ls.diskStamp = fileStamp(ls.path) // our own write needs no re-merge
}
