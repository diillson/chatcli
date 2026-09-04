/*
 * ChatCLI - Long-term memory: durable persistence primitives.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Every memory substore (facts, profile, topics, projects, patterns) persists
 * as a small JSON file, sealed when encryption at rest is on. Two disciplines
 * keep those files from ever costing the user their accumulated memory:
 *
 *   1. Writes are atomic (temp file + rename in the same directory), so a
 *      crash mid-write can never leave a half-written file under the store's
 *      real name.
 *   2. A file that fails to parse at load is QUARANTINED (renamed aside with a
 *      ".corrupt" suffix), never left in place. Leaving it in place is how
 *      memory used to vanish: the store started empty, the next persist
 *      overwrote the only copy of the data, and no error was ever surfaced.
 */
package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/diillson/chatcli/pkg/atrest"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// atomicWriteFile writes data to path via a same-directory temp file, fsync
// and an atomic rename (utils.AtomicWriteFile), so readers, crashes and power
// loss only ever observe the old or the new content — never a torn write.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	sealed, err := atrest.SealAt(path, data)
	if err != nil {
		return err
	}
	return utils.AtomicWriteFile(path, sealed, perm)
}

// readStoreFile reads a store file and opens it when it is sealed
// (encryption at rest, CHATCLI_ENCRYPTION_KEY). Plaintext passes through.
// A sealed file that cannot be opened (key unset, wrong, or retired without
// CHATCLI_ENCRYPTION_KEY_PREVIOUS) returns a *SealedStoreError so the store
// LOCKS instead of starting empty and overwriting the user's memory.
func readStoreFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- fixed store filename under the memory dir
	if err != nil {
		return nil, err
	}
	if atrest.IsEncrypted(data) {
		plain, oerr := atrest.OpenAt(path, data)
		if oerr != nil {
			return nil, &SealedStoreError{Path: path, Err: oerr}
		}
		return plain, nil
	}
	return data, nil
}

// SealedStoreError marks a sealed store file this process cannot open.
type SealedStoreError struct {
	Path string
	Err  error
}

func (e *SealedStoreError) Error() string {
	return "memory store sealed and unreadable (" + filepath.Base(e.Path) + "): " + e.Err.Error()
}

func (e *SealedStoreError) Unwrap() error { return e.Err }

// storeLatch is the read-only latch every substore carries: once a sealed
// file could not be opened, the store keeps whatever is in memory but
// refuses every persist, so the sealed bytes on disk survive a process
// without the key. Sessions already fail loudly in that case; memory used
// to load empty (debug log only) and overwrite the file on the first fact.
type storeLatch struct {
	mu     sync.Mutex
	reason string
	path   string
}

// lockIfSealed latches the store when err is a sealed-store error and
// returns whether it did.
func (l *storeLatch) lockIfSealed(err error, logger *zap.Logger, store string) bool {
	var se *SealedStoreError
	if !errors.As(err, &se) {
		return false
	}
	l.mu.Lock()
	first := l.reason == ""
	l.reason = se.Err.Error()
	l.path = se.Path
	l.mu.Unlock()
	registerLockedStore(store, se.Path, se.Err.Error())
	if first && logger != nil {
		logger.Error("memory store is sealed and cannot be opened; store LOCKED read-only to protect it (set "+atrest.EnvKey+" or "+atrest.EnvPreviousKeys+")",
			zap.String("store", store), zap.String("path", se.Path), zap.Error(se.Err))
	}
	return true
}

// locked reports whether persists must be refused.
func (l *storeLatch) locked() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reason != ""
}

var (
	lockedStoresMu sync.Mutex
	lockedStores   = map[string]LockedStore{}
)

// LockedStore describes one latched store for the UI.
type LockedStore struct {
	Store  string
	Path   string
	Reason string
}

func registerLockedStore(store, path, reason string) {
	lockedStoresMu.Lock()
	lockedStores[path] = LockedStore{Store: store, Path: path, Reason: reason}
	lockedStoresMu.Unlock()
}

// LockedStores lists the stores latched read-only in this process, sorted
// by path (empty when every store opened).
func LockedStores() []LockedStore {
	lockedStoresMu.Lock()
	defer lockedStoresMu.Unlock()
	out := make([]LockedStore, 0, len(lockedStores))
	for _, l := range lockedStores {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// resetLockedStoresForTest clears the registry (tests only).
func resetLockedStoresForTest() {
	lockedStoresMu.Lock()
	lockedStores = map[string]LockedStore{}
	lockedStoresMu.Unlock()
}

// quarantineCorrupt moves an unparseable store file aside as
// "<path>.corrupt" (or a timestamped variant when a previous quarantine
// already exists, so an older recovery copy is never clobbered). It returns
// the quarantine path for logging. The caller decides how to proceed —
// typically by starting the store empty, which is now safe because the
// original bytes remain recoverable.
func quarantineCorrupt(path string) (string, error) {
	dst := path + ".corrupt"
	// #nosec G703 -- path is a fixed store filename under the operator-configured
	// memory directory (~/.chatcli/memory), never user/model input; same
	// precedent as the CCR store paths in cli/compress/ccr.go.
	if _, err := os.Stat(dst); err == nil {
		dst = fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
	}
	// #nosec G703 -- see above: operator-configured store path, not tainted input.
	if err := os.Rename(path, dst); err != nil {
		return "", err
	}
	return dst, nil
}
