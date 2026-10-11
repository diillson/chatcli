/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// browserNavigationAllowed is the policy for every document the @browser
// session loads: the URL given to open or show, each redirect hop, each
// frame and link the page follows, and a tab switched to. Only http,
// https and about:blank pass; file:, data:, javascript: and the browser's
// own chrome: pages read local state that the read sandbox otherwise
// protects. An http(s) target goes through the shared SSRF guard, so the
// cloud metadata and link-local addresses are refused even when a
// hostname resolves to them.
func browserNavigationAllowed(raw string) error {
	u := strings.TrimSpace(raw)
	if strings.EqualFold(u, "about:blank") {
		return nil
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("scheme %q is not allowed: @browser opens only http, https and about:blank", parsed.Scheme)
	}
	return webHostTargetAllowed(u, parsed)
}

// browserArtifactDir is where screenshot and pdf write: the session
// scratch directory (CHATCLI_AGENT_TMPDIR) or, without one, a directory
// under the system temp dir. It matches how @webfetch confines save_path.
func browserArtifactDir() string {
	if scratch := strings.TrimSpace(os.Getenv("CHATCLI_AGENT_TMPDIR")); scratch != "" {
		return scratch
	}
	return filepath.Join(os.TempDir(), "chatcli-browser")
}

// browserArtifactPath confines a screenshot or pdf file to
// browserArtifactDir. Only the base name of a requested path is kept, so a
// model cannot write the image over a file of its choosing; the default
// name is generated from prefix and ext. An existing entry that is not a
// regular file (a planted symlink) is refused.
func browserArtifactPath(requested, prefix, ext string) (string, error) {
	dir := browserArtifactDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := filepath.Base(strings.TrimSpace(requested))
	if requested == "" || name == "." || name == ".." || name == string(filepath.Separator) {
		name = fmt.Sprintf("%s-%d%s", prefix, time.Now().UnixMilli(), ext)
	}
	path := filepath.Join(dir, name)
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if filepath.Dir(absPath) != absDir {
		return "", fmt.Errorf("file %q escapes the browser output directory %s", requested, dir)
	}
	if info, err := os.Lstat(absPath); err == nil && !info.Mode().IsRegular() {
		return "", errors.New("refusing to write through " + absPath + ": it exists and is not a regular file")
	}
	return absPath, nil
}
