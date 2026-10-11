/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// snapshotRelPath validates one snapshot file path and returns it in clean
// forward-slash form. The path is later joined with filepath.FromSlash, so
// it is checked in that same OS form: filepath.IsLocal rejects absolute
// paths, ".." escapes, and on Windows drive letters, UNC prefixes and
// reserved names. Backslashes and colons are refused on every platform —
// a snapshot is produced for any OS, and on Windows those characters are
// separators or volume/stream markers.
func snapshotRelPath(p string) (string, error) {
	if p == "" || strings.ContainsAny(p, "\\:\x00") {
		return "", fmt.Errorf("unsafe path in snapshot: %q", p)
	}
	cleaned := path.Clean(p)
	if !filepath.IsLocal(filepath.FromSlash(cleaned)) {
		return "", fmt.Errorf("unsafe path in snapshot: %q", p)
	}
	return cleaned, nil
}

// snapshotDigest is the content digest a skills.sh snapshot carries in its
// "hash" field: SHA-256, hex encoded, over the files in the order the
// snapshot lists them, each contributing its path, a NUL byte, its contents
// and a NUL byte. The scheme was checked against live skills.sh snapshots.
func snapshotDigest(files []skillsshSnapshotFile) string {
	h := sha256.New()
	for _, f := range files {
		h.Write([]byte(f.Path))
		h.Write([]byte{0})
		h.Write([]byte(f.Contents))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// verifySnapshotHash checks the snapshot's own hash when it carries one. A
// hash in an unrecognized form or one that does not match the files is an
// error: the snapshot is refused rather than installed unverified.
func verifySnapshotHash(files []skillsshSnapshotFile, hash string) error {
	if hash == "" {
		return nil
	}
	if raw, err := hex.DecodeString(hash); err != nil || len(raw) != sha256.Size {
		return fmt.Errorf("snapshot hash %q is not a hex SHA-256; refusing to install", hash)
	}
	if got := snapshotDigest(files); !strings.EqualFold(got, hash) {
		return fmt.Errorf("snapshot content does not match its hash (expected %s, got %s); refusing to install", strings.ToLower(hash), got)
	}
	return nil
}
