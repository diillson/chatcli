/*
 * Package park: durable snapshots for the agent ReAct loop.
 *
 * The park subsystem lets a tool call (typically @park) suspend the
 * interactive agent loop, persist the loop's full state to disk, and
 * release the terminal back to the user. A scheduler job drives the
 * eventual resume, which re-enters the same loop from where it stopped.
 *
 * Files in ~/.chatcli/parked/ are the source of truth for parked state.
 * The scheduler WAL is what guarantees the resume fires; the snapshot is
 * what the agent loop reads to rehydrate itself.
 */
package park

import (
	"os"
	"path/filepath"
)

// envOverride lets tests redirect the snapshot directory without
// touching $HOME. Production callers should not set this.
const envOverride = "CHATCLI_PARK_DIR"

// Dir returns the on-disk directory used to persist snapshots. It honors
// CHATCLI_PARK_DIR for tests; otherwise it resolves to
// $XDG_CONFIG_HOME/chatcli/parked (or the OS-appropriate equivalent).
//
// The directory is created with 0o700 if missing — snapshots embed the
// chat history, so they must not leak to other users on the host.
func Dir() (string, error) {
	if override := os.Getenv(envOverride); override != "" {
		if err := os.MkdirAll(override, 0o700); err != nil {
			return "", err
		}
		return override, nil
	}
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cfgDir, "chatcli", "parked")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// pathFor returns the on-disk path for a token. Callers must validate
// the token before calling this — Save and Load do.
func pathFor(token string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, token+".json"), nil
}
