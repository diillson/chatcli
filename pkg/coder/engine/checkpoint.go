/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * checkpoint.go — shadow-git safety net for the coder engine.
 *
 * Before every mutating subcommand (write, patch, multipatch, exec) the
 * engine snapshots the workspace into a SHADOW git repository that lives
 * under the user's ChatCLI home — a separate GIT_DIR pointed at the
 * workspace as its work tree. The user's own .git is never touched (git
 * skips nested .git directories), the project's .gitignore is honored, and
 * `checkpoint restore` rewinds the tree to any snapshot — a stronger net
 * than the per-file .bak rollback, covering multi-file edits and exec side
 * effects.
 *
 * Guardrails: snapshots are best-effort (a checkpoint failure never blocks
 * the edit), throttled (at most one per checkpointMinInterval), disabled
 * when git is absent, and switched off entirely with
 * CHATCLI_CODER_CHECKPOINTS=off.
 */
package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// checkpointEnv is the kill switch for automatic workspace snapshots.
const checkpointEnv = "CHATCLI_CODER_CHECKPOINTS"

// checkpointMinInterval throttles automatic snapshots: a burst of writes in
// one batch produces one checkpoint, not one per file.
const checkpointMinInterval = 15 * time.Second

// checkpointMaxList bounds a listing.
const checkpointMaxList = 30

var (
	checkpointMu   sync.Mutex
	lastCheckpoint = map[string]time.Time{} // workspace root -> last snapshot
)

// checkpointsEnabled honors CHATCLI_CODER_CHECKPOINTS=0|false|off|no.
func checkpointsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(checkpointEnv))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// shadowGitDir resolves the shadow repository path for a workspace root.
func shadowGitDir(root string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(home, ".chatcli", "checkpoints", hex.EncodeToString(sum[:8])), nil
}

// shadowGit runs one git command against the shadow repo/worktree pair.
func shadowGit(gitDir, workTree string, args ...string) (string, error) {
	base := []string{"--git-dir=" + gitDir, "--work-tree=" + workTree}
	cmd := exec.Command("git", append(base, args...)...) // #nosec G204 -- fixed git binary; args are engine-built, never model-provided verbatim
	// The shadow repo must never inherit the user's hooks or identity
	// requirements: commits are local bookkeeping, not authored history.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=chatcli-checkpoint", "GIT_AUTHOR_EMAIL=checkpoint@chatcli.local",
		"GIT_COMMITTER_NAME=chatcli-checkpoint", "GIT_COMMITTER_EMAIL=checkpoint@chatcli.local",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ensureShadowRepo initializes the shadow repository once.
func ensureShadowRepo(gitDir, workTree string) error {
	if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(gitDir, 0o750); err != nil {
		return err
	}
	_, err := shadowGit(gitDir, workTree, "init", "-q")
	return err
}

// snapshotWorkspace takes one checkpoint of the workspace (best-effort
// semantics belong to the caller). label lands in the commit subject.
func snapshotWorkspace(root, label string) error {
	gitDir, err := shadowGitDir(root)
	if err != nil {
		return err
	}
	if err := ensureShadowRepo(gitDir, root); err != nil {
		return err
	}
	if _, err := shadowGit(gitDir, root, "add", "-A"); err != nil {
		return err
	}
	// Nothing staged → nothing to record; keep the log meaningful.
	if _, err := shadowGit(gitDir, root, "diff", "--cached", "--quiet"); err == nil {
		return nil
	}
	_, err = shadowGit(gitDir, root, "commit", "-q", "-m",
		fmt.Sprintf("checkpoint before %s (%s)", label, time.Now().Format(time.RFC3339)))
	return err
}

// SnapshotWorkspace exposes one shadow-git checkpoint of root for external
// orchestrators (the task-graph engine snapshots before each executor task).
// Honors the checkpoint kill switch; best-effort semantics belong to the
// caller. Unlike autoCheckpoint it is NOT throttled — call it per meaningful
// boundary, never in a loop.
func SnapshotWorkspace(root, label string) error {
	if !checkpointsEnabled() {
		return nil
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil
	}
	return snapshotWorkspace(root, label)
}

// autoCheckpoint snapshots the workspace before a mutating subcommand:
// best-effort, throttled, and silent on success. Failures are reported to
// the human stream once but never block the edit.
func (e *Engine) autoCheckpoint(label string) {
	if !checkpointsEnabled() {
		return
	}
	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	root := e.WorkspaceRoot
	if root == "" {
		return
	}
	checkpointMu.Lock()
	if time.Since(lastCheckpoint[root]) < checkpointMinInterval {
		checkpointMu.Unlock()
		return
	}
	lastCheckpoint[root] = time.Now()
	checkpointMu.Unlock()

	if err := snapshotWorkspace(root, label); err != nil {
		e.errorf("checkpoint skipped: %v\n", err)
	}
}

// handleCheckpoint is the model/user-facing surface: list snapshots, take one
// explicitly, or restore the workspace to one.
func (e *Engine) handleCheckpoint(args []string) error {
	fs := flag.NewFlagSet("checkpoint", flag.ContinueOnError)
	list := fs.Bool("list", false, "")
	create := fs.Bool("create", false, "")
	restore := fs.String("restore", "", "")
	label := fs.String("label", "manual", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	// Positional spelling: "checkpoint list" / "checkpoint restore <hash>".
	if rest := fs.Args(); len(rest) > 0 {
		switch rest[0] {
		case "list":
			*list = true
		case "create":
			*create = true
		case "restore":
			if len(rest) > 1 {
				*restore = rest[1]
			}
		}
	}

	root := e.WorkspaceRoot
	gitDir, err := shadowGitDir(root)
	if err != nil {
		return err
	}

	switch {
	case *restore != "":
		if _, statErr := os.Stat(filepath.Join(gitDir, "HEAD")); statErr != nil {
			return fmt.Errorf("no checkpoints exist for this workspace yet")
		}
		if out, err := shadowGit(gitDir, root, "checkout", *restore, "--", "."); err != nil {
			return fmt.Errorf("restore %s: %s", *restore, out)
		}
		e.printf("Workspace restored to checkpoint %s. Files added AFTER that checkpoint still exist (restore never deletes untracked files).\n", *restore)
		return nil

	case *create:
		if err := snapshotWorkspace(root, *label); err != nil {
			return fmt.Errorf("checkpoint: %w", err)
		}
		e.printf("Checkpoint recorded.\n")
		return nil

	case *list:
		fallthrough
	default:
		if _, statErr := os.Stat(filepath.Join(gitDir, "HEAD")); statErr != nil {
			e.printf("No checkpoints for this workspace yet — they are recorded automatically before write/patch/exec.\n")
			return nil
		}
		out, err := shadowGit(gitDir, root, "log", fmt.Sprintf("--max-count=%d", checkpointMaxList),
			"--pretty=format:%h  %ad  %s", "--date=format:%Y-%m-%d %H:%M:%S")
		if err != nil {
			return fmt.Errorf("checkpoint list: %s", out)
		}
		if strings.TrimSpace(out) == "" {
			e.printf("No checkpoints recorded yet.\n")
			return nil
		}
		e.printf("Checkpoints (newest first — restore with checkpoint --restore <hash>):\n%s\n", out)
		return nil
	}
}
