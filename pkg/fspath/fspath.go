/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package fspath provides filesystem path comparison helpers that are correct
// across operating systems. It is stdlib-only so that leaf packages with a
// no-external-dependency contract (e.g. pkg/coder/engine) can import it.
package fspath

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// normalize cleans a path and puts it in canonical comparison form: forward
// slashes everywhere, lowercased on Windows (whose filesystems are
// case-insensitive).
func normalize(p string) string {
	p = filepath.ToSlash(filepath.Clean(p))
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

// WithinBoundary reports whether target is root itself or a path contained
// inside root. Both are cleaned and compared with normalized separators, so a
// target like "C:/Users/x/file" matches a root of "C:\Users\x". On Windows the
// comparison is case-insensitive, matching filesystem semantics.
//
// Callers must pass absolute (and, where relevant, symlink-resolved) paths;
// this function does not resolve them.
func WithinBoundary(target, root string) bool {
	if target == "" || root == "" {
		return false
	}
	t := normalize(target)
	r := normalize(root)
	if t == r {
		return true
	}
	if !strings.HasSuffix(r, "/") {
		r += "/"
	}
	return strings.HasPrefix(t, r)
}

// Equal reports whether two paths refer to the same location after cleaning,
// separator normalization and — on Windows — case folding. It does not touch
// the filesystem (no symlink resolution).
func Equal(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	return normalize(a) == normalize(b)
}

// WindowsSystemRoots returns OS-critical directories that automated tools
// must never touch on Windows — the counterpart of the unix /etc, /proc &co.
// blocklists, which never match backslashed paths. Resolved from the
// environment so non-standard installs are still covered. Empty on other
// operating systems.
func WindowsSystemRoots() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return []string{root}
}
