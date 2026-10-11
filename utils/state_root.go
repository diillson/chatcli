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
	"strings"
)

// Owner-only modes for the state root (~/.chatcli) and what it holds.
const (
	privateDirMode  fs.FileMode = 0o700
	privateFileMode fs.FileMode = 0o600
)

// sensitiveStateDirs hold conversations, credentials, memory or derived
// data; they are made owner-only again on every start.
var sensitiveStateDirs = []string{
	"sessions", "memory", "transcripts", "costs", "ccr", "checkpoints",
	"attachments", "contexts", "mcp", "mcp_oauth", "tenants", "scheduler",
	"reflexion", "pulse", "taskgraph", "cache", "hub",
	"browser", "agent-logs", "openai-cache", "exports",
}

// hubDBSuffixes are the files SQLite keeps beside a WAL-mode database.
var hubDBSuffixes = []string{"", "-wal", "-shm", "-journal"}

// privateStateRoot creates or repairs ~/.chatcli and returns its real path.
func privateStateRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return ensurePrivateRoot(filepath.Join(home, ".chatcli"))
}

// EnsureStateRoot creates the state root owner-only (0700) and, on every
// start, repairs what earlier versions left readable by other users: the
// root and the sensitive subdirectories go back to 0700, and every regular
// file directly under the root (logs, the hub database and its WAL/SHM, the
// credential store, history) to 0600, as does a hub database configured
// outside the root through CHATCLI_HUB_DB.
//
// Repairs only ever remove permission bits, never follow a symbolic link,
// and only touch entries owned by the current user. It runs before the
// logger, so it reports problems through its error instead of logging.
func EnsureStateRoot() error {
	resolved, err := privateStateRoot()
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range sensitiveStateDirs {
		if err := restrictToOwner(filepath.Join(resolved, name), privateDirMode, true); err != nil {
			errs = append(errs, err)
		}
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		errs = append(errs, err)
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			if err := restrictToOwner(filepath.Join(resolved, e.Name()), privateFileMode, false); err != nil {
				errs = append(errs, err)
			}
		}
	}
	// A hub database outside the root (server/hub cannot be imported here).
	if custom := strings.TrimSpace(os.Getenv("CHATCLI_HUB_DB")); custom != "" {
		if err := restrictSQLiteSiblings(custom); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ensurePrivateRoot creates root at 0700 when missing and tightens it when
// it exists. A root that is a symbolic link (a user who keeps the state on
// another volume) is resolved, and its target must be a directory owned by
// the current user. Returns the resolved directory.
func ensurePrivateRoot(root string) (string, error) {
	fi, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(root, privateDirMode); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("create %s: %w", root, err)
		}
		fi, err = os.Lstat(root)
	}
	if err != nil {
		return "", err
	}
	resolved := root
	if fi.Mode()&fs.ModeSymlink != 0 {
		if resolved, err = filepath.EvalSymlinks(root); err != nil {
			return "", err
		}
		if fi, err = os.Lstat(resolved); err != nil {
			return "", err
		}
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", resolved)
	}
	if !ownedByCurrentUser(fi) {
		return "", fmt.Errorf("%s is not owned by the current user; refusing to use it", resolved)
	}
	return resolved, restrictToOwner(resolved, privateDirMode, true)
}

// PrivateStateDir returns the subdirectory name of the state root, created
// owner-only. It is the place for files a feature would otherwise drop in
// the shared system temp directory under a fixed name, where another local
// user can pre-create the directory or plant a symbolic link in it.
func PrivateStateDir(name string) (string, error) {
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return "", fmt.Errorf("invalid state directory name %q", name)
	}
	resolved, err := privateStateRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(resolved, name)
	if err := os.Mkdir(dir, privateDirMode); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 || !ownedByCurrentUser(fi) {
		return "", fmt.Errorf("%s is not a private directory owned by the current user", dir)
	}
	return dir, restrictToOwner(dir, privateDirMode, true)
}

// RestrictSQLiteFiles makes a SQLite database owner-only before it is
// opened: the database file is created at 0600 when missing (SQLite gives
// the -wal and -shm files it creates the mode of the database file), and
// the database and any existing -wal, -shm or -journal file are tightened
// to 0600. Symbolic links and files of other users are left alone.
func RestrictSQLiteFiles(dbPath string) error {
	if dbPath == "" || strings.HasPrefix(dbPath, ":memory:") || strings.HasPrefix(dbPath, "file:") {
		return nil
	}
	if err := createPrivateFile(dbPath); err != nil {
		return err
	}
	return restrictSQLiteSiblings(dbPath)
}

// restrictSQLiteSiblings tightens an existing database and its -wal, -shm
// and -journal files to 0600 without creating anything.
func restrictSQLiteSiblings(dbPath string) error {
	var errs []error
	for _, suffix := range hubDBSuffixes {
		if err := restrictToOwner(dbPath+suffix, privateFileMode, false); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// createPrivateFile creates path empty at 0600 when it does not exist. An
// existing path (including a symbolic link) is not opened.
func createPrivateFile(path string) error {
	f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode) // #nosec G304 -- operator-configured database path
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return f.Close()
}
