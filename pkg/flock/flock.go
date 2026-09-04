/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Cross-process file locks for the shared stores.
 *
 * The REPL, the gateway daemon and the MCP server merge-then-write the
 * same JSON stores (memory indexes, contexts, vector caches). Without a
 * lock, two processes could both read, both merge, and the second write
 * would erase the first's new entries until that process persisted
 * again. Lock takes an exclusive advisory lock on a sidecar next to the
 * store (<path>.lock) for the merge + write critical section.
 */
package flock

import (
	"os"
	"path/filepath"
)

// Lock takes the exclusive lock guarding path and returns the release
// function. A lock that cannot be taken (read-only directory, exotic
// filesystem) degrades to a no-op release: the store still works as it
// did before locks existed.
func Lock(path string) func() {
	if path == "" {
		return func() {}
	}
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o750); err != nil {
		return func() {}
	}
	f, err := os.OpenFile(filepath.Clean(lockPath), os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 G703 -- sidecar next to a store path the caller owns
	if err != nil {
		return func() {}
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		return func() {}
	}
	return func() {
		_ = unlockFile(f)
		_ = f.Close()
	}
}
