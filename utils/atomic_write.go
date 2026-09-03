/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"os"
	"path/filepath"
	"runtime"
)

// AtomicWriteFile persists data under path so that readers, crashes and
// power loss only ever observe the old or the new content — never a torn or
// half-flushed file.
//
// Sequence: same-directory temp file → write → fsync(file) → chmod → close →
// rename over path → fsync(dir). The rename alone makes the swap atomic with
// respect to other processes; the two fsyncs are what make it durable: without
// fsync(file) a rename can land on disk before the bytes it points to, and
// without fsync(dir) the directory entry itself may not survive a crash. The
// directory sync is best-effort — Windows does not support opening a
// directory for fsync, and some filesystems reject it — so its failure is
// ignored once the file itself has been synced.
//
// Every durable store in ChatCLI (sessions, contexts, memory, task graphs)
// delegates here so the durability contract lives in one place.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir flushes a directory's entries to disk after a rename. Best-effort by
// design: the caller's data is already durable at this point, and platforms
// that cannot fsync a directory (Windows) or filesystems that refuse it must
// not turn a successful write into an error.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	d, err := os.Open(dir) // #nosec G304 -- dir is the parent of a store path the caller already validated
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
