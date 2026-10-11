//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// ownedByCurrentUser reports whether fi belongs to the effective user.
func ownedByCurrentUser(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}

// restrictToOwner removes every permission bit outside want from path when
// it is a directory (dir) or regular file (!dir) owned by the current user.
// A missing path, a symbolic link, another type of file or another user's
// file is left alone. The mode is changed through a descriptor opened with
// O_NOFOLLOW and checked to be the entry that was inspected, so a link
// swapped in between can never redirect the change.
func restrictToOwner(path string, want fs.FileMode, dir bool) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 || fi.IsDir() != dir || (!dir && !fi.Mode().IsRegular()) {
		return nil
	}
	if !ownedByCurrentUser(fi) {
		return nil
	}
	perm := fi.Mode().Perm()
	tight := perm & want
	if tight == perm {
		return nil
	}
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) // #nosec G304 -- entries of the user's own state root
	if err != nil {
		// A file the owner cannot even read (mode 0200 and the like)
		// already keeps everyone else out of its content.
		if errors.Is(err, fs.ErrPermission) {
			return nil
		}
		return fmt.Errorf("restrict %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	cur, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(fi, cur) {
		return nil
	}
	if err := f.Chmod(tight); err != nil {
		return fmt.Errorf("restrict %s: %w", path, err)
	}
	return nil
}
