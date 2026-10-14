/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Executing the exact bytes that verified.
//
// The signature check reads the plugin file and verifies those bytes. If
// the loader then executed the plugin by its path, whoever can write the
// plugins directory could swap the file between the check and every later
// exec (--metadata, --schema, each tool call). Re-hashing before each exec
// would only narrow that window, never close it. The root fix is to never
// run the path at all: the verified bytes are written once to a private,
// content-addressed file and that file is what runs. Its name is the
// SHA-256 of its content and it lives in a 0700 directory owned by this
// user, so what executes is, by construction, what verified.

// verifiedCacheDirName is the directory, under the ChatCLI cache, holding
// the private copies of verified plugins.
const verifiedCacheDirName = "verified-plugins"

// verifiedCacheGrace keeps an unreferenced copy around for a while: another
// ChatCLI process may still be running it until its own reload.
const verifiedCacheGrace = 24 * time.Hour

// verifiedCacheDir derives the private cache from the plugins directory:
// <pluginsDir>/../cache/verified-plugins, i.e. ~/.chatcli/cache/... in
// production and a sibling temp dir in tests.
// VerifiedCacheDir is where verified binaries of plugins in pluginsDir are
// staged before they run: <pluginsDir>/../cache/verified-plugins.
func VerifiedCacheDir(pluginsDir string) string {
	return filepath.Join(filepath.Dir(pluginsDir), "cache", verifiedCacheDirName)
}

// stageVerifiedBinary writes data to a private file named by its SHA-256
// and returns that path. The original extension is kept so Windows can
// still execute it. An existing copy is reused only when its content is
// byte-identical.
func stageVerifiedBinary(cacheDir string, data []byte, originalName string) (string, error) {
	if err := ensurePrivateDir(cacheDir); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	final := filepath.Join(cacheDir, hex.EncodeToString(sum[:])+strings.ToLower(filepath.Ext(originalName)))

	if existing, err := os.ReadFile(final); err == nil && bytes.Equal(existing, data) { // #nosec G304 -- content-addressed name inside our private cache dir
		now := time.Now()
		_ = os.Chtimes(final, now, now) // mark as in use for the GC grace period
		return final, nil
	}

	tmp, err := os.CreateTemp(cacheDir, ".stage-*")
	if err != nil {
		return "", fmt.Errorf("staging verified plugin: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("staging verified plugin: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("staging verified plugin: %w", err)
	}
	if err := os.Chmod(tmpName, 0o700); err != nil { // #nosec G302 -- owner-only executable
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("staging verified plugin: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("staging verified plugin: %w", err)
	}
	return final, nil
}

// ensurePrivateDir creates dir as a real directory (not a symlink) readable
// only by this user.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating verified plugin cache: %w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("verified plugin cache: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("verified plugin cache %s is not a directory", dir)
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("verified plugin cache: %w", err)
		}
	}
	return nil
}

// pruneVerifiedCache removes copies no loaded plugin references once they
// are past the grace period. Best-effort.
func pruneVerifiedCache(cacheDir string, inUse map[string]struct{}) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(cacheDir, e.Name())
		if _, used := inUse[p]; used {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > verifiedCacheGrace {
			_ = os.Remove(p)
		}
	}
}

// ErrPluginNotTrusted reports a binary the signature policy refused.
var ErrPluginNotTrusted = errors.New("plugin signature cannot be trusted")

// ErrPluginQuarantined reports an unsigned binary still inside its
// quarantine window.
var ErrPluginQuarantined = errors.New("plugin held in quarantine")

// ExecDecision is the signature policy's verdict for one binary.
type ExecDecision struct {
	// ExecPath is what to run: a private staged copy of the exact bytes
	// that verified, or the original path when an unsigned binary is
	// tolerated.
	ExecPath string
	// Unsigned is true when the binary runs only because
	// CHATCLI_ALLOW_UNSIGNED_PLUGINS is set.
	Unsigned bool
}

// ResolveExec applies the plugin signature policy to binaryPath, the same
// one tool plugins and provider plugins share:
//
//   - signed by a trusted key: stage the verified bytes under cacheDir and
//     run that copy, never the path again;
//   - unsigned, or signed with no trusted key to check against: refused
//     unless unsigned plugins are allowed, and then still held while the
//     quarantine window (when configured) runs;
//   - anything else (bad signature, untrusted signer): refused.
func (v *PluginVerifier) ResolveExec(q *Quarantine, cacheDir, binaryPath string) (ExecDecision, error) {
	status, verified, err := v.inspectWithBytes(binaryPath)
	switch status {
	case StatusVerified:
		staged, err := stageVerifiedBinary(cacheDir, verified, filepath.Base(binaryPath))
		if err != nil {
			return ExecDecision{}, err
		}
		return ExecDecision{ExecPath: staged}, nil
	case StatusUnsigned, StatusUnverifiable:
		// Unverifiable joins unsigned on purpose: with no trusted key on
		// this machine a signature is not evidence of anything.
		if !v.AllowsUnsigned() {
			return ExecDecision{}, fmt.Errorf("%w: unsigned plugins are not allowed%s", ErrPluginNotTrusted, errSuffix(err))
		}
		if admitted, remaining := q.Admit(binaryPath); !admitted {
			return ExecDecision{}, fmt.Errorf("%w for another %s", ErrPluginQuarantined, remaining.Round(time.Second))
		}
		return ExecDecision{ExecPath: binaryPath, Unsigned: true}, nil
	default:
		return ExecDecision{}, fmt.Errorf("%w%s", ErrPluginNotTrusted, errSuffix(err))
	}
}

// errSuffix renders an optional cause as ": cause" (empty for nil).
func errSuffix(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}
