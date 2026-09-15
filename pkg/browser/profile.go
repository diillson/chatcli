/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * profile.go — where the @browser session keeps its cookies and storage.
 *
 * Default is a throwaway profile deleted on close: safe for untrusted pages
 * and the reason a login the user has in their everyday Chrome never leaks
 * into the agent's browser. The flip side is that a login performed in the
 * agent's window is lost when ChatCLI exits. CHATCLI_BROWSER_PROFILE opts
 * into a persistent profile owned by ChatCLI (never the user's real Chrome
 * profile — Chrome locks that while it runs, and sharing it would hand the
 * agent every session the user has open).
 */
package browser

import (
	"os"
	"path/filepath"
	"strings"
)

// ProfileEnv selects the profile the browser runs on. Empty (default) is a
// throwaway profile removed on close. A directory path uses that directory;
// 1/true/on/yes/persistent use the default persistent location under the
// ChatCLI home.
const ProfileEnv = "CHATCLI_BROWSER_PROFILE"

// defaultProfileRel is the persistent profile location relative to the
// user's home directory.
const defaultProfileRel = ".chatcli/browser/profile"

// persistentProfileDir resolves ProfileEnv: "" when the session should use a
// throwaway profile, otherwise the directory to persist cookies and storage
// in across ChatCLI runs.
func persistentProfileDir() string {
	raw := strings.TrimSpace(os.Getenv(ProfileEnv))
	if raw == "" {
		return ""
	}
	switch strings.ToLower(raw) {
	case "0", "false", "off", "no", "none", "ephemeral", "temp":
		return ""
	case "1", "true", "on", "yes", "persistent", "default":
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		return filepath.Join(home, filepath.FromSlash(defaultProfileRel))
	}
	if strings.HasPrefix(raw, "~") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			raw = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(raw, "~"), string(os.PathSeparator)))
		}
	}
	return filepath.Clean(raw)
}
