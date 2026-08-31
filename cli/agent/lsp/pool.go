/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Session-scoped language-server pool — the runtime behind the @lsp tool.
 *
 * The one-shot /lsp command spawns a server per invocation, which is fine for
 * a single manual check but unacceptable for an agent: gopls or rust-analyzer
 * take seconds to index a module, and a model chasing references across ten
 * files would pay that cold start ten times. The pool keeps one initialized
 * client per (project root, server command), synchronizes documents into it
 * (didOpen once, versioned didChange after), reaps idle servers, and shuts
 * everything down when the session ends.
 */
package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	// poolIdleTTL is how long an unused language server stays alive. Long
	// enough to survive a conversation lull; short enough that a session
	// that moved on from Rust doesn't keep rust-analyzer resident forever.
	// Field data moved this from 5 to 15 minutes: agent sessions routinely
	// pause longer than 5 minutes between edits (model turns, the user
	// reading output), and every reap re-bills the next post-edit check
	// with a full cold start and re-index.
	poolIdleTTL = 15 * time.Minute

	// maxDocumentBytes bounds one didOpen/didChange payload. Files past this
	// are almost certainly generated artifacts a language server would choke
	// on anyway; the tool reports a clear error instead of stalling.
	maxDocumentBytes = 2 << 20 // 2MiB

	// warmupTimeout bounds one background warm-up spawn kicked off by
	// AcquireReady. Generous — nothing waits on it — but finite, so a hung
	// server binary cannot pin a goroutine for the whole session.
	warmupTimeout = 30 * time.Second
)

// rootMarkers, in priority order, identify a project root when walking up
// from a file. The server must be rooted at the module/workspace level or
// cross-file navigation (references, definitions in other packages) silently
// degrades to single-file scope.
var rootMarkers = []string{"go.work", "go.mod", "Cargo.toml", "package.json", "pom.xml", "build.gradle", ".git"}

// Pool manages initialized language-server clients for a session.
type Pool struct {
	logger *zap.Logger

	mu      sync.Mutex
	entries map[string]*poolEntry
	// warming tracks in-flight background warm-ups (single-flight per key).
	warming map[string]bool
	// closed marks a pool torn down by Close: no further spawns, so a
	// warm-up racing session shutdown can never leak a live server.
	closed bool

	// spawn is the client factory — swapped by tests for a pipe-backed fake.
	spawn func(ctx context.Context, spec ServerSpec, logger *zap.Logger) (*Client, error)
	// now is the clock — swapped by tests to exercise idle reaping.
	now func() time.Time
}

// poolEntry is one live server plus its document-synchronization state.
type poolEntry struct {
	client   *Client
	lastUse  time.Time
	docs     map[string]docState // uri → last synced state
	rootURI  string
	spec     ServerSpec
	poolKey  string
	shutdown bool
}

type docState struct {
	version     int
	contentHash string
}

// NewPool returns an empty pool. A nil logger upgrades to a no-op.
func NewPool(logger *zap.Logger) *Pool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Pool{
		logger:  logger,
		entries: map[string]*poolEntry{},
		spawn:   Spawn,
		now:     time.Now,
	}
}

// Session is an acquired (client, document) pair ready for navigation calls.
type Session struct {
	Client *Client
	URI    string
	Root   string
}

// Acquire returns a ready Session for absPath: the pooled client for the
// file's project root and language server (spawned and initialized on first
// use), with the document opened or re-synchronized when its content changed
// since the last call. Callers must pass an absolute, cleaned path.
func (p *Pool) Acquire(ctx context.Context, absPath string) (*Session, error) {
	spec, ok := ServerForFile(absPath)
	if !ok {
		return nil, fmt.Errorf("no language server configured for %q files (supported: %s)",
			filepath.Ext(absPath), strings.Join(SupportedExtensions(), " "))
	}

	data, err := os.ReadFile(absPath) //#nosec G304 -- agent-requested source file, same trust boundary as @read
	if err != nil {
		return nil, err
	}
	if len(data) > maxDocumentBytes {
		return nil, fmt.Errorf("file too large for language-server analysis (%d bytes, limit %d)", len(data), maxDocumentBytes)
	}

	root := findProjectRoot(absPath)
	key := root + "\x00" + strings.Join(spec.Command, " ")

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, fmt.Errorf("lsp: pool is closed")
	}
	p.reapIdleLocked()

	entry, ok := p.entries[key]
	if !ok {
		// The server must OUTLIVE this call: exec.CommandContext ties the
		// subprocess lifetime to its context, so spawning with the request
		// ctx (or any ctx we cancel) kills the server the moment we return —
		// the E2E symptom is an instant "broken pipe" on initialize. The pool
		// owns the lifetime (idle reap + Close); only inherit values.
		client, spawnErr := p.spawn(context.WithoutCancel(ctx), spec, p.logger)
		if spawnErr != nil {
			return nil, fmt.Errorf("start %s: %w (install it or override via %s)", spec.Command[0], spawnErr, spec.EnvKey)
		}
		rootURI := "file://" + root
		if initErr := client.Initialize(rootURI); initErr != nil {
			client.Shutdown()
			return nil, fmt.Errorf("initialize %s: %w", spec.Command[0], initErr)
		}
		entry = &poolEntry{
			client:  client,
			docs:    map[string]docState{},
			rootURI: rootURI,
			spec:    spec,
			poolKey: key,
		}
		p.entries[key] = entry
		p.logger.Info("lsp: language server started",
			zap.String("server", spec.Command[0]), zap.String("root", root))
	}
	entry.lastUse = p.now()

	uri := "file://" + absPath
	if syncErr := entry.syncDocument(uri, spec.LanguageID, string(data)); syncErr != nil {
		return nil, syncErr
	}
	return &Session{Client: entry.client, URI: uri, Root: root}, nil
}

// AcquireReady returns a ready Session for absPath only when the pooled
// server for its (project root, language) is ALREADY initialized — it never
// spawns synchronously. A cold pool instead kicks off a single-flight
// background warm-up and reports not-ready, so advisory callers (the
// post-edit diagnostics hook above all) skip cheaply now and find a warm
// server on their next call. TryLock keeps the same promise against a busy
// pool: if another caller holds it — typically a cold spawn in flight —
// report not-ready rather than queue behind seconds of initialization.
func (p *Pool) AcquireReady(absPath string) (*Session, bool) {
	spec, ok := ServerForFile(absPath)
	if !ok {
		return nil, false
	}
	data, err := os.ReadFile(absPath) //#nosec G304 -- agent-requested source file, same trust boundary as Acquire
	if err != nil || len(data) > maxDocumentBytes {
		return nil, false
	}
	root := findProjectRoot(absPath)
	key := root + "\x00" + strings.Join(spec.Command, " ")

	if !p.mu.TryLock() {
		return nil, false
	}
	defer p.mu.Unlock()
	if p.closed {
		return nil, false
	}
	p.reapIdleLocked()

	entry, ok := p.entries[key]
	if !ok {
		p.warmLocked(key, absPath)
		return nil, false
	}
	entry.lastUse = p.now()

	uri := "file://" + absPath
	if syncErr := entry.syncDocument(uri, spec.LanguageID, string(data)); syncErr != nil {
		return nil, false
	}
	return &Session{Client: entry.client, URI: uri, Root: root}, true
}

// warmLocked starts one background warm-up for key unless one is already in
// flight. Caller holds p.mu. The goroutine reuses Acquire wholesale — same
// spawn, same initialization, same entry bookkeeping — and discards the
// session; only the pooled server matters. Acquire's own closed-pool guard
// makes a warm-up racing Close shut down cleanly instead of leaking.
func (p *Pool) warmLocked(key, absPath string) {
	if p.warming == nil {
		p.warming = map[string]bool{}
	}
	if p.warming[key] {
		return
	}
	p.warming[key] = true
	go func() {
		defer func() {
			p.mu.Lock()
			delete(p.warming, key)
			p.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), warmupTimeout)
		defer cancel()
		if _, err := p.Acquire(ctx, absPath); err != nil {
			p.logger.Debug("lsp: background warm-up failed", zap.Error(err))
		}
	}()
}

// syncDocument opens the document on first use and sends a versioned full
// didChange when its content differs from the last synced state. Unchanged
// content is a no-op, so repeated navigation over the same file costs nothing.
func (e *poolEntry) syncDocument(uri, languageID, text string) error {
	hash := contentHash(text)
	state, opened := e.docs[uri]
	switch {
	case !opened:
		if err := e.client.DidOpen(uri, languageID, text); err != nil {
			return err
		}
		e.docs[uri] = docState{version: 1, contentHash: hash}
	case state.contentHash != hash:
		next := state.version + 1
		// Diagnostics for the old content are stale the moment the buffer
		// changes; clear so waiters get the fresh publish.
		e.client.ClearDiagnostics(uri)
		if err := e.client.DidChange(uri, text, next); err != nil {
			return err
		}
		e.docs[uri] = docState{version: next, contentHash: hash}
	}
	return nil
}

// reapIdleLocked shuts down servers idle past the TTL. Caller holds p.mu.
func (p *Pool) reapIdleLocked() {
	cutoff := p.now().Add(-poolIdleTTL)
	for key, e := range p.entries {
		if e.lastUse.Before(cutoff) {
			p.logger.Info("lsp: reaping idle language server",
				zap.String("server", e.spec.Command[0]))
			e.shutdown = true
			e.client.Shutdown()
			delete(p.entries, key)
		}
	}
}

// Close shuts down every pooled server. Called when the session ends.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for key, e := range p.entries {
		e.shutdown = true
		e.client.Shutdown()
		delete(p.entries, key)
	}
}

// findProjectRoot walks up from path looking for a workspace marker so the
// server is rooted at module level — required for cross-file navigation.
// Falls back to the file's directory when no marker exists.
func findProjectRoot(absPath string) string {
	dir := filepath.Dir(absPath)
	for cur := dir; ; {
		for _, m := range rootMarkers {
			if _, err := os.Stat(filepath.Join(cur, m)); err == nil {
				return cur
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return dir
		}
		cur = parent
	}
}

// contentHash is a cheap change detector for document sync (FNV-1a).
func contentHash(s string) string {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	var h uint64 = offset64
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return fmt.Sprintf("%016x", h)
}
