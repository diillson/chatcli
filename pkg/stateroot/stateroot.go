/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package stateroot resolves the directory ChatCLI keeps its state in:
// ~/.chatcli by default, or the directory CHATCLI_STATE_DIR names. Every
// store under it (sessions, memory, transcripts, costs, plugins, skills,
// the credential store, the global .env) follows the same root, so a
// second root is a second, fully separate installation that shares
// nothing but the binary and the OS keychain.
//
// Directories that belong to a project (a workspace's .chatcli/rules,
// .chatcli/hooks.json, .chatcli/commands) are not state and never move.
// It is stdlib-only so leaf packages with a no-external-dependency
// contract (e.g. pkg/coder/engine) can import it.
package stateroot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Env names the state directory in place of ~/.chatcli.
const Env = "CHATCLI_STATE_DIR"

// DefaultName is the state directory's name under the home directory.
const DefaultName = ".chatcli"

// Dir returns the state directory: CHATCLI_STATE_DIR when set (a leading
// ~ is expanded and a relative path is made absolute against the current
// directory), ~/.chatcli otherwise. It does not create the directory.
func Dir() (string, error) {
	if Custom() {
		return Under(""), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", errors.New("home directory is empty")
	}
	return Under(home), nil
}

// Under returns the state directory for a caller that already resolved
// the home directory: CHATCLI_STATE_DIR when set, home/.chatcli otherwise.
func Under(home string) string {
	raw := strings.TrimSpace(os.Getenv(Env))
	if raw == "" {
		return filepath.Join(home, DefaultName)
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") || strings.HasPrefix(raw, `~\`) {
		h := home
		if h == "" {
			h, _ = os.UserHomeDir()
		}
		raw = filepath.Join(h, raw[1:])
	}
	if abs, err := filepath.Abs(raw); err == nil {
		return abs
	}
	return filepath.Clean(raw)
}

// Custom reports whether CHATCLI_STATE_DIR moves the state directory away
// from ~/.chatcli.
func Custom() bool {
	return strings.TrimSpace(os.Getenv(Env)) != ""
}
