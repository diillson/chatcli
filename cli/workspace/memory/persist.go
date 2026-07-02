/*
 * ChatCLI - Long-term memory: durable persistence primitives.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Every memory substore (facts, profile, topics, projects, patterns) persists
 * as a small JSON file. Two disciplines keep those files from ever costing the
 * user their accumulated memory:
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
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// atomicWriteFile writes data to path via a same-directory temp file and an
// atomic rename, so readers (and crashes) only ever observe the old or the new
// content — never a torn write.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
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
	return nil
}

// quarantineCorrupt moves an unparseable store file aside as
// "<path>.corrupt" (or a timestamped variant when a previous quarantine
// already exists, so an older recovery copy is never clobbered). It returns
// the quarantine path for logging. The caller decides how to proceed —
// typically by starting the store empty, which is now safe because the
// original bytes remain recoverable.
func quarantineCorrupt(path string) (string, error) {
	dst := path + ".corrupt"
	if _, err := os.Stat(dst); err == nil {
		dst = fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
	}
	if err := os.Rename(path, dst); err != nil {
		return "", err
	}
	return dst, nil
}
