/*
 * ChatCLI - Persisted knowledge-graph cache
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The knowledge graph used to be derived from scratch on every consumer call
 * — six call sites, each paying store copies plus a skills-directory
 * filesystem walk. This cache makes the graph a first-class derived index:
 *
 *   - the built graph is held in memory behind an atomic pointer and served
 *     as an IMMUTABLE snapshot (rebuilds construct a fresh graph and swap
 *     the pointer; nothing ever mutates a served graph in place);
 *   - content mutations in the source stores mark the cache dirty via the
 *     changeNotifier taps (see change_notify.go) and the fact-removal hook;
 *     the first reader after a write rebuilds, under single-flight;
 *   - the graph is persisted to <memoryDir>/graph.json so the NEXT process/
 *     boot adopts it instantly instead of paying the first build — guarded
 *     by a schema version and a source fingerprint;
 *   - staleness policy is the vindex one (discard + rebuild), NOT the
 *     facts.go quarantine/tombstone protocol: every byte of the graph is
 *     re-derivable from the source stores, so a corrupt, stale or
 *     version-mismatched file is simply removed. Cross-process writes are
 *     last-writer-wins for the same reason.
 */
package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/pkg/knowledge"
	"go.uber.org/zap"
)

// graphSchemaVersion gates persisted files: any format/derivation change
// bumps it, and older files are discarded and rebuilt.
const graphSchemaVersion = 1

// graphFingerprintTTL bounds how often the fast path re-checks the source
// fingerprint. External edits (skills changed in an editor, sessions written
// by another process) are picked up within this window even when no
// in-process tap fired.
const graphFingerprintTTL = 5 * time.Minute

// graphFile is the on-disk envelope around the serialized graph.
type graphFile struct {
	SchemaVersion int             `json:"schema_version"`
	Fingerprint   string          `json:"fingerprint"`
	SavedAt       time.Time       `json:"saved_at"`
	Graph         json.RawMessage `json:"graph"`
}

// GraphCache owns the cached graph snapshot and its persistence. Inert (all
// methods no-ops returning nil) until SetSource attaches a builder.
type GraphCache struct {
	mu          sync.Mutex // guards rebuild + fingerprint bookkeeping
	snap        atomic.Pointer[knowledge.Graph]
	dirty       atomic.Bool
	build       func() *knowledge.Graph
	fingerprint func() string

	lastFP        string
	lastFPCheckMu sync.Mutex
	lastFPCheck   time.Time

	path            string
	persistInFlight atomic.Bool
	logger          *zap.Logger
}

// NewGraphCache creates an inert cache rooted at memoryDir. It becomes live
// when SetSource wires a builder.
func NewGraphCache(memoryDir string, logger *zap.Logger) *GraphCache {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &GraphCache{
		path:   filepath.Join(memoryDir, "graph.json"),
		logger: logger,
	}
}

// MarkDirty flags the snapshot as stale. Cheap (one atomic store) — this is
// what every changeNotifier tap and removal hook resolves to.
func (gc *GraphCache) MarkDirty() {
	if gc != nil {
		gc.dirty.Store(true)
	}
}

// SetSource wires the builder and fingerprint functions, then tries to adopt
// the persisted file: a valid, fingerprint-matching, current-schema graph
// becomes the initial snapshot (instant boot, zero build cost); anything
// else is discarded and the cache starts dirty.
func (gc *GraphCache) SetSource(build func() *knowledge.Graph, fingerprint func() string) {
	if gc == nil || build == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.build = build
	gc.fingerprint = fingerprint

	fp := ""
	if fingerprint != nil {
		fp = fingerprint()
	}
	gc.lastFP = fp
	gc.lastFPCheck = time.Now()

	if g, ok := gc.loadPersisted(fp); ok {
		gc.snap.Store(g)
		gc.dirty.Store(false)
		return
	}
	gc.dirty.Store(true)
}

// loadPersisted adopts graph.json when valid and current. Any failure mode
// (missing, corrupt, wrong schema, stale fingerprint) discards the file —
// the graph is re-derivable, so there is nothing to preserve.
func (gc *GraphCache) loadPersisted(currentFP string) (*knowledge.Graph, bool) {
	data, err := os.ReadFile(gc.path)
	if err != nil {
		return nil, false // missing is the common first-boot case
	}
	discard := func(reason string) {
		gc.logger.Debug("memory graph cache discarded", zap.String("reason", reason))
		_ = os.Remove(gc.path)
	}
	var env graphFile
	if err := json.Unmarshal(data, &env); err != nil {
		discard("corrupt envelope")
		return nil, false
	}
	if env.SchemaVersion != graphSchemaVersion {
		discard("schema version mismatch")
		return nil, false
	}
	if env.Fingerprint != currentFP {
		discard("stale fingerprint")
		return nil, false
	}
	g, err := knowledge.UnmarshalGraph(env.Graph)
	if err != nil {
		discard("corrupt graph payload")
		return nil, false
	}
	gc.logger.Debug("memory graph cache adopted from disk",
		zap.Int("nodes", g.Len()), zap.Int("edges", g.Edges()))
	return g, true
}

// Snapshot returns the current graph, rebuilding first when dirty or when
// the periodic fingerprint re-check detects an external change. Returns nil
// when no builder is wired (feature off). The returned graph is an immutable
// snapshot — consumers must treat it as read-only (they all do; rebuilds
// swap in a fresh graph rather than mutating).
func (gc *GraphCache) Snapshot() *knowledge.Graph {
	if gc == nil {
		return nil
	}
	gc.mu.Lock()
	build := gc.build
	gc.mu.Unlock()
	if build == nil {
		return nil
	}

	gc.checkFingerprintTTL()

	if !gc.dirty.Load() {
		if g := gc.snap.Load(); g != nil {
			return g
		}
	}

	// Slow path: single-flight rebuild. Concurrent readers block briefly on
	// mu and adopt the fresh snapshot via the double-check.
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if !gc.dirty.Load() {
		if g := gc.snap.Load(); g != nil {
			return g
		}
	}
	g := gc.build()
	if g == nil {
		return gc.snap.Load()
	}
	gc.snap.Store(g)
	gc.dirty.Store(false)
	fp := ""
	if gc.fingerprint != nil {
		fp = gc.fingerprint()
		gc.lastFP = fp
	}
	gc.persistAsync(g, fp)
	return g
}

// checkFingerprintTTL recomputes the source fingerprint at most once per
// graphFingerprintTTL (stat-only, no parsing) and marks the cache dirty on
// change — the backstop for edits made outside this process.
func (gc *GraphCache) checkFingerprintTTL() {
	gc.lastFPCheckMu.Lock()
	due := time.Since(gc.lastFPCheck) > graphFingerprintTTL
	if due {
		gc.lastFPCheck = time.Now()
	}
	gc.lastFPCheckMu.Unlock()
	if !due {
		return
	}
	gc.mu.Lock()
	fingerprint := gc.fingerprint
	last := gc.lastFP
	gc.mu.Unlock()
	if fingerprint == nil {
		return
	}
	if fp := fingerprint(); fp != last {
		gc.MarkDirty()
	}
}

// persistAsync writes the envelope off the hot path. Single-flight via CAS;
// a skipped write is harmless (the next rebuild persists again) and the
// atomic rename keeps readers from ever seeing a partial file.
func (gc *GraphCache) persistAsync(g *knowledge.Graph, fp string) {
	if !gc.persistInFlight.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer gc.persistInFlight.Store(false)
		payload, err := g.MarshalGraph()
		if err != nil {
			gc.logger.Warn("memory graph cache: marshal failed", zap.Error(err))
			return
		}
		env := graphFile{
			SchemaVersion: graphSchemaVersion,
			Fingerprint:   fp,
			SavedAt:       time.Now(),
			Graph:         payload,
		}
		data, err := json.Marshal(env)
		if err != nil {
			gc.logger.Warn("memory graph cache: envelope marshal failed", zap.Error(err))
			return
		}
		if err := atomicWriteFile(gc.path, data, 0o600); err != nil {
			gc.logger.Warn("memory graph cache: persist failed", zap.Error(err))
		}
	}()
}

// Stats reports the cached snapshot's size without ever triggering a build
// (safe to call from status screens). ok is false when no snapshot exists.
func (gc *GraphCache) Stats() (nodes, edges int, ok bool) {
	if gc == nil {
		return 0, 0, false
	}
	g := gc.snap.Load()
	if g == nil {
		return 0, 0, false
	}
	return g.Len(), g.Edges(), true
}
