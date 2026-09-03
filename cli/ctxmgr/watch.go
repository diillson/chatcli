/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Filesystem watch for contexts: /context watch <name> keeps a context in
 * step with its source paths. Events are debounced per context and
 * collapse into one RefreshContext, which itself does nothing when the
 * stamps say nothing changed (an editor's temp files, a touch).
 */
package ctxmgr

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/diillson/chatcli/utils"
	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
)

// watchDebounce is how long a burst of events settles before one refresh.
const watchDebounce = 1500 * time.Millisecond

// RefreshNotifier receives the outcome of every watcher-driven refresh.
type RefreshNotifier func(name string, rep RefreshReport, err error)

// ContextWatcher drives RefreshContext from fsnotify events.
type ContextWatcher struct {
	m       *Manager
	logger  *zap.Logger
	notify  RefreshNotifier
	watcher *fsnotify.Watcher

	mu      sync.Mutex
	byDir   map[string]string // watched dir → context name
	timers  map[string]*time.Timer
	names   map[string][]string // context name → its dirs
	closed  bool
	ctx     context.Context
	cancel  context.CancelFunc
	started sync.Once
}

// NewContextWatcher creates an idle watcher; Watch starts it.
func NewContextWatcher(m *Manager, logger *zap.Logger, notify RefreshNotifier) *ContextWatcher {
	if logger == nil {
		logger = zap.NewNop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &ContextWatcher{m: m, logger: logger, notify: notify, byDir: map[string]string{}, timers: map[string]*time.Timer{}, names: map[string][]string{}, ctx: ctx, cancel: cancel}
}

func (w *ContextWatcher) ensureStarted() error {
	var err error
	w.started.Do(func() {
		w.watcher, err = fsnotify.NewWatcher()
		if err != nil {
			return
		}
		go w.loop()
	})
	if err != nil {
		return err
	}
	if w.watcher == nil {
		return os.ErrClosed
	}
	return nil
}

// Watch starts watching a context's source paths (directories recursively,
// skipping the usual noise directories). Idempotent per context.
func (w *ContextWatcher) Watch(name string) error {
	paths, ok := w.m.SourcePathsOf(name)
	if !ok {
		return ErrContextNotFound
	}
	if len(paths) == 0 {
		return ErrNoSourcePaths
	}
	if err := w.ensureStarted(); err != nil {
		return err
	}
	dirs := watchDirsFor(paths)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return os.ErrClosed
	}
	if _, already := w.names[name]; already {
		return nil
	}
	added := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if err := w.watcher.Add(d); err != nil {
			w.logger.Debug("context watch: cannot watch dir", zap.String("dir", d), zap.Error(err))
			continue
		}
		w.byDir[d] = name
		added = append(added, d)
	}
	w.names[name] = added
	return nil
}

// Unwatch stops watching a context. Reports whether it was watched.
func (w *ContextWatcher) Unwatch(name string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	dirs, ok := w.names[name]
	if !ok {
		return false
	}
	for _, d := range dirs {
		if w.watcher != nil {
			_ = w.watcher.Remove(d)
		}
		delete(w.byDir, d)
	}
	if t := w.timers[name]; t != nil {
		t.Stop()
		delete(w.timers, name)
	}
	delete(w.names, name)
	return true
}

// Watching lists the watched context names, sorted.
func (w *ContextWatcher) Watching() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.names))
	for n := range w.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Close stops everything.
func (w *ContextWatcher) Close() {
	w.mu.Lock()
	w.closed = true
	for _, t := range w.timers {
		t.Stop()
	}
	w.timers = map[string]*time.Timer{}
	w.mu.Unlock()
	w.cancel()
	if w.watcher != nil {
		_ = w.watcher.Close()
	}
}

func (w *ContextWatcher) loop() {
	for {
		select {
		case <-w.ctx.Done():
			return
		case ev, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			w.schedule(ev.Name)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			w.logger.Debug("context watch error", zap.Error(err))
		}
	}
}

// schedule debounces a refresh for the context owning the event's dir; a
// new subdirectory is added to the watch so nested edits keep arriving.
func (w *ContextWatcher) schedule(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	name := w.ownerOf(path)
	if name == "" {
		return
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() && !utils.ShouldSkipDir(filepath.Base(path)) {
		// A created directory may already hold a subtree (mkdir -p, a
		// checkout, an unpack): watch all of it, not just the top.
		for _, d := range watchDirsFor([]string{path}) {
			if _, already := w.byDir[d]; already {
				continue
			}
			if err := w.watcher.Add(d); err == nil {
				w.byDir[d] = name
				w.names[name] = append(w.names[name], d)
			}
		}
	}
	if t := w.timers[name]; t != nil {
		t.Stop()
	}
	w.timers[name] = time.AfterFunc(watchDebounce, func() { w.refresh(name) })
}

func (w *ContextWatcher) ownerOf(path string) string {
	dir := path
	for {
		if name, ok := w.byDir[dir]; ok {
			return name
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func (w *ContextWatcher) refresh(name string) {
	w.mu.Lock()
	delete(w.timers, name)
	closed := w.closed
	w.mu.Unlock()
	if closed {
		return
	}
	_, rep, err := w.m.RefreshContext(w.ctx, name)
	if w.notify != nil && (err != nil || rep.Dirty()) {
		w.notify(name, rep, err)
	}
}

// watchDirsFor turns source paths into the directories to watch: a file's
// parent, a directory and all its subdirectories (noise dirs skipped).
func watchDirsFor(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			add(filepath.Dir(p))
			continue
		}
		_ = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			if path != p && utils.ShouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			add(path)
			return nil
		})
	}
	return out
}
