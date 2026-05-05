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
//
// Trust model: CHATCLI_PARK_DIR is read from the calling process's own
// environment. Whoever can set that variable is already running as the
// chatcli user with full file-system access — no privilege boundary is
// crossed by honoring it. We still apply filepath.Clean to normalize
// the input so the on-disk layout matches user expectation.
func Dir() (string, error) {
	if override := os.Getenv(envOverride); override != "" {
		clean := filepath.Clean(override)
		// #nosec G304 -- the override path comes from CHATCLI_PARK_DIR
		// in the chatcli process's own environment; gosec's taint
		// analysis cannot model that the variable is operator-supplied
		// and stays inside the operator's trust boundary. The path is
		// normalized via filepath.Clean above; no traversal escape is
		// possible because there is no privileged target to escape to.
		if err := os.MkdirAll(clean, 0o700); err != nil {
			return "", err
		}
		return clean, nil
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
