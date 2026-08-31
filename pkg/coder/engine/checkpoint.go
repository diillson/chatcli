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
 *
 * Scale guardrails (post-mortem, Aug 2026): a session whose workspace was
 * the user's HOME directory made every exec/write run `git add -A` over the
 * entire home — a hash of ~Library, VM images and every project on disk that
 * never completed, leaving gigabytes of aborted tmp packs and an orphaned
 * index.lock behind while the command spinner sat frozen. Snapshots are now
 * additionally bounded by four independent mechanisms, each safe on its own:
 *
 *   1. Root guard — the filesystem root, the home directory, or any
 *      ancestor of home never auto-checkpoints (warned once per session).
 *   2. Deadline — every snapshot runs under a hard timeout
 *      (CHATCLI_CODER_CHECKPOINT_TIMEOUT seconds; small for automatic
 *      snapshots, larger for explicit `checkpoint create`).
 *   3. Circuit breaker — failures back off exponentially and repeated
 *      failures disable automatic snapshots for the rest of the session,
 *      so a doomed workspace is never re-scanned forever.
 *   4. Artifact hygiene — index.lock and tmp_pack_* leftovers from killed
 *      runs are swept, so aborted snapshots cannot accumulate garbage or
 *      wedge the next attempt.
 */
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/pkg/fspath"
)

// checkpointEnv is the kill switch for automatic workspace snapshots.
const checkpointEnv = "CHATCLI_CODER_CHECKPOINTS"

// checkpointTimeoutEnv overrides the snapshot deadline, in whole seconds.
// One knob for both surfaces: automatic snapshots use it as-is, explicit
// `checkpoint create` calls fall back to the larger manual default when the
// variable is unset.
const checkpointTimeoutEnv = "CHATCLI_CODER_CHECKPOINT_TIMEOUT"

// checkpointMinInterval throttles automatic snapshots: a burst of writes in
// one batch produces one checkpoint, not one per file.
const checkpointMinInterval = 15 * time.Second

const (
	// checkpointAutoTimeout bounds one automatic snapshot. It runs inline
	// before the user's command, so the budget is deliberately small: a
	// healthy project-sized `git add -A` finishes in well under a second,
	// and anything slower must never hold an exec or write hostage.
	checkpointAutoTimeout = 10 * time.Second

	// checkpointManualTimeout bounds an explicit `checkpoint create`, where
	// the user asked for the snapshot and a first-ever add of a large
	// project may legitimately need more room.
	checkpointManualTimeout = 60 * time.Second

	// checkpointMaxTimeout clamps the env override so a typo cannot park a
	// command for hours.
	checkpointMaxTimeout = 10 * time.Minute

	// checkpointMaxBackoff caps the exponential failure backoff.
	checkpointMaxBackoff = 30 * time.Minute

	// checkpointBreakerThreshold is how many consecutive failures disable
	// automatic snapshots for the rest of the session.
	checkpointBreakerThreshold = 3

	// checkpointStaleLockAge / checkpointStaleTmpAge decide when leftovers
	// of a dead snapshot attempt (crash, Ctrl+C, deadline kill) are swept.
	// Old enough that a live concurrent ChatCLI process is never disturbed.
	checkpointStaleLockAge = 10 * time.Minute
	checkpointStaleTmpAge  = time.Hour
)

// checkpointMaxList bounds a listing.
const checkpointMaxList = 30

// checkpointTracker holds the per-workspace-root snapshot state for the
// whole process: throttle, failure backoff, session circuit breaker,
// in-flight guard, one-time warnings and the stale-artifact sweep marker.
// It must be process-global — a fresh Engine is built for every plugin
// call, so nothing here can live on the Engine itself.
type checkpointTracker struct {
	mu    sync.Mutex
	now   func() time.Time // swapped by tests
	roots map[string]*checkpointRootState
}

type checkpointRootState struct {
	lastAttempt time.Time
	failures    int
	inFlight    bool
	disabled    bool
	warned      bool
	swept       bool
}

var checkpoints = newCheckpointTracker()

func newCheckpointTracker() *checkpointTracker {
	return &checkpointTracker{now: time.Now, roots: map[string]*checkpointRootState{}}
}

// state returns (creating if needed) the entry for root. Caller holds t.mu.
func (t *checkpointTracker) state(root string) *checkpointRootState {
	st, ok := t.roots[root]
	if !ok {
		st = &checkpointRootState{}
		t.roots[root] = st
	}
	return st
}

// checkpointBackoff is the wait required after n consecutive failures:
// the base throttle doubled per failure, capped at checkpointMaxBackoff.
func checkpointBackoff(failures int) time.Duration {
	d := checkpointMinInterval
	for i := 0; i < failures; i++ {
		d *= 2
		if d >= checkpointMaxBackoff {
			return checkpointMaxBackoff
		}
	}
	return d
}

// begin reports whether an automatic snapshot of root may start now and, when
// it may, reserves the slot: the throttle window opens from this instant and
// concurrent attempts on the same root are refused until finish runs.
func (t *checkpointTracker) begin(root string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.state(root)
	if st.disabled || st.inFlight {
		return false
	}
	if t.now().Sub(st.lastAttempt) < checkpointBackoff(st.failures) {
		return false
	}
	st.lastAttempt = t.now()
	st.inFlight = true
	return true
}

// finish records the outcome of a snapshot attempt started by begin. It
// reports whether this failure was the one that tripped the session breaker,
// so the caller can warn exactly once.
func (t *checkpointTracker) finish(root string, err error) (tripped bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.state(root)
	st.inFlight = false
	if err == nil {
		st.failures = 0
		return false
	}
	if errors.Is(err, context.Canceled) {
		// The user interrupted the command (Ctrl+C propagates into the
		// snapshot ctx); that says nothing about workspace health, so it
		// neither counts toward the breaker nor resets the streak.
		return false
	}
	st.failures++
	if st.failures >= checkpointBreakerThreshold && !st.disabled {
		st.disabled = true
		return true
	}
	return false
}

// warnOnce reports whether the caller should emit the one-per-session
// warning for root, flipping the marker on first use.
func (t *checkpointTracker) warnOnce(root string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.state(root)
	if st.warned {
		return false
	}
	st.warned = true
	return true
}

// shouldSweep reports whether the stale-artifact sweep still needs to run
// for root this session, flipping the marker on first use.
func (t *checkpointTracker) shouldSweep(root string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.state(root)
	if st.swept {
		return false
	}
	st.swept = true
	return true
}

// checkpointsEnabled honors CHATCLI_CODER_CHECKPOINTS=0|false|off|no.
func checkpointsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(checkpointEnv))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// resolveCheckpointTimeout returns the snapshot deadline: the env override
// in whole seconds when valid, def otherwise. Clamped so a typo can neither
// zero the guard nor stretch it past checkpointMaxTimeout.
func resolveCheckpointTimeout(def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(checkpointTimeoutEnv))
	if raw == "" {
		return def
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return def
	}
	d := time.Duration(secs) * time.Second
	if d > checkpointMaxTimeout {
		return checkpointMaxTimeout
	}
	return d
}

// unsafeCheckpointRoot reports whether root is too broad to ever snapshot:
// the filesystem root, the user's home directory, or any ancestor of home
// (e.g. /Users, C:\Users). Snapshotting such a root means git-hashing
// essentially the whole disk — in the field this produced gigabytes of
// aborted pack files and commands that never returned. The returned reason
// is human-readable and stable enough to log.
func unsafeCheckpointRoot(root string) (bool, string) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return true, "workspace root cannot be resolved"
	}
	abs = filepath.Clean(abs)
	if abs == filepath.Dir(abs) {
		return true, "workspace is the filesystem root"
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false, ""
	}
	home = filepath.Clean(home)
	if fspath.Equal(abs, home) {
		return true, "workspace is the home directory"
	}
	if fspath.WithinBoundary(home, abs) {
		return true, "workspace contains the home directory"
	}
	return false, ""
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

// shadowGit runs one git command against the shadow repo/worktree pair,
// bounded by ctx — when the deadline fires the git process is killed, and
// the caller is responsible for cleaning what it left behind (see
// cleanupAbortedSnapshot).
func shadowGit(ctx context.Context, gitDir, workTree string, args ...string) (string, error) {
	base := []string{"--git-dir=" + gitDir, "--work-tree=" + workTree}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...) // #nosec G204 -- fixed git binary; args are engine-built, never model-provided verbatim
	// A killed git can leave children holding the output pipes (the reason
	// the deadline exists at all is processes that outstay their welcome);
	// WaitDelay guarantees CombinedOutput returns shortly after the kill
	// instead of blocking on pipe EOF forever.
	cmd.WaitDelay = 2 * time.Second
	// The shadow repo must never inherit the user's hooks or identity
	// requirements: commits are local bookkeeping, not authored history.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=chatcli-checkpoint", "GIT_AUTHOR_EMAIL=checkpoint@chatcli.local",
		"GIT_COMMITTER_NAME=chatcli-checkpoint", "GIT_COMMITTER_EMAIL=checkpoint@chatcli.local",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() != nil {
		// Surface the deadline, not git's SIGKILL noise — the caller and the
		// breaker treat "too slow" as its own failure class.
		return strings.TrimSpace(string(out)), fmt.Errorf("snapshot deadline exceeded: %w", ctx.Err())
	}
	return strings.TrimSpace(string(out)), err
}

// ensureShadowRepo initializes the shadow repository once.
func ensureShadowRepo(ctx context.Context, gitDir, workTree string) error {
	if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(gitDir, 0o750); err != nil {
		return err
	}
	_, err := shadowGit(ctx, gitDir, workTree, "init", "-q")
	return err
}

// sweepStaleArtifacts removes leftovers of snapshot attempts that died
// without cleanup (deadline kill, Ctrl+C, crash): an index.lock older than
// checkpointStaleLockAge and objects/pack/tmp_pack_* files older than
// checkpointStaleTmpAge. Ages are generous so a live concurrent ChatCLI
// process is never disturbed. Best-effort: errors are ignored — git
// re-creates anything it actually needs.
func sweepStaleArtifacts(gitDir string, now time.Time) {
	lock := filepath.Join(gitDir, "index.lock")
	if info, err := os.Stat(lock); err == nil && now.Sub(info.ModTime()) > checkpointStaleLockAge {
		_ = os.Remove(lock)
	}
	packDir := filepath.Join(gitDir, "objects", "pack")
	entries, err := os.ReadDir(packDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "tmp_pack_") {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) <= checkpointStaleTmpAge {
			continue
		}
		_ = os.Remove(filepath.Join(packDir, entry.Name()))
	}
}

// cleanupAbortedSnapshot removes what OUR just-killed snapshot attempt left
// behind: an index.lock and tmp packs created at or after the attempt
// started. Anything older may belong to another process and is left for the
// age-based sweep.
func cleanupAbortedSnapshot(gitDir string, started time.Time) {
	cutoff := started.Add(-time.Second) // mtime granularity slack
	lock := filepath.Join(gitDir, "index.lock")
	if info, err := os.Stat(lock); err == nil && info.ModTime().After(cutoff) {
		_ = os.Remove(lock)
	}
	packDir := filepath.Join(gitDir, "objects", "pack")
	entries, err := os.ReadDir(packDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "tmp_pack_") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(packDir, entry.Name()))
	}
}

// snapshotWorkspace takes one checkpoint of the workspace (best-effort
// semantics belong to the caller). label lands in the commit subject. The
// ctx deadline is the hard budget; a run killed by it cleans up after
// itself so the next attempt starts from a healthy repo.
func snapshotWorkspace(ctx context.Context, root, label string) error {
	gitDir, err := shadowGitDir(root)
	if err != nil {
		return err
	}
	if checkpoints.shouldSweep(root) {
		sweepStaleArtifacts(gitDir, checkpoints.now())
	}
	started := checkpoints.now()
	if err := ensureShadowRepo(ctx, gitDir, root); err != nil {
		return err
	}
	if _, err := shadowGit(ctx, gitDir, root, "add", "-A"); err != nil {
		if ctx.Err() != nil {
			cleanupAbortedSnapshot(gitDir, started)
		}
		return err
	}
	// Nothing staged → nothing to record; keep the log meaningful.
	if _, err := shadowGit(ctx, gitDir, root, "diff", "--cached", "--quiet"); err == nil {
		return nil
	} else if ctx.Err() != nil {
		return fmt.Errorf("snapshot deadline exceeded: %w", ctx.Err())
	}
	_, err = shadowGit(ctx, gitDir, root, "commit", "-q", "-m",
		fmt.Sprintf("checkpoint before %s (%s)", label, time.Now().Format(time.RFC3339)))
	if err != nil && ctx.Err() != nil {
		cleanupAbortedSnapshot(gitDir, started)
	}
	return err
}

// CheckpointWorkspace exposes one shadow-git checkpoint of root for external
// orchestrators (the task-graph engine snapshots before each executor task).
// Honors the checkpoint kill switch; best-effort semantics belong to the
// caller. Unlike autoCheckpoint it is NOT throttled — call it per meaningful
// boundary, never in a loop.
func CheckpointWorkspace(root, label string) error {
	return CheckpointWorkspaceCtx(context.Background(), root, label)
}

// CheckpointWorkspaceCtx is CheckpointWorkspace with caller-owned
// cancellation: the snapshot deadline is layered onto ctx, so an
// orchestrator abort also aborts the snapshot. Prefer this from any call
// site that already holds a context.
func CheckpointWorkspaceCtx(ctx context.Context, root, label string) error {
	if !checkpointsEnabled() {
		return nil
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil
	}
	if unsafe, _ := unsafeCheckpointRoot(root); unsafe {
		// Same best-effort contract as a disabled kill switch: skipping is
		// not an error, and the alternative (hashing the whole disk) is.
		return nil
	}
	snapCtx, cancel := context.WithTimeout(ctx, resolveCheckpointTimeout(checkpointManualTimeout))
	defer cancel()
	return snapshotWorkspace(snapCtx, root, label)
}

// autoCheckpoint snapshots the workspace before a mutating subcommand:
// best-effort, throttled, and silent on success. Failures are reported to
// the human stream once but never block the edit. Every scale guardrail
// funnels through here — root guard, deadline, backoff, breaker.
func (e *Engine) autoCheckpoint(ctx context.Context, label string) {
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

	if unsafe, reason := unsafeCheckpointRoot(root); unsafe {
		if checkpoints.warnOnce(root) {
			e.errorf("[checkpoint] automatic snapshots disabled: %s — run @coder from a project directory to get them back (details: CHATCLI_CODER_CHECKPOINTS in /config)\n", reason)
		}
		return
	}

	if !checkpoints.begin(root) {
		return
	}
	snapCtx, cancel := context.WithTimeout(ctx, resolveCheckpointTimeout(checkpointAutoTimeout))
	defer cancel()
	err := snapshotWorkspace(snapCtx, root, label)
	tripped := checkpoints.finish(root, err)
	if err != nil && !errors.Is(err, context.Canceled) {
		e.errorf("checkpoint skipped: %v\n", err)
	}
	if tripped {
		e.errorf("[checkpoint] %d consecutive snapshot failures — automatic snapshots disabled for the rest of the session (raise %s for big workspaces, or set %s=off)\n",
			checkpointBreakerThreshold, checkpointTimeoutEnv, checkpointEnv)
	}
}

// handleCheckpoint is the model/user-facing surface: list snapshots, take one
// explicitly, or restore the workspace to one.
func (e *Engine) handleCheckpoint(ctx context.Context, args []string) error {
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
	opCtx, cancel := context.WithTimeout(ctx, resolveCheckpointTimeout(checkpointManualTimeout))
	defer cancel()

	switch {
	case *restore != "":
		if _, statErr := os.Stat(filepath.Join(gitDir, "HEAD")); statErr != nil {
			return fmt.Errorf("no checkpoints exist for this workspace yet")
		}
		if out, err := shadowGit(opCtx, gitDir, root, "checkout", *restore, "--", "."); err != nil {
			return fmt.Errorf("restore %s: %s", *restore, out)
		}
		e.printf("Workspace restored to checkpoint %s. Files added AFTER that checkpoint still exist (restore never deletes untracked files).\n", *restore)
		return nil

	case *create:
		if unsafe, reason := unsafeCheckpointRoot(root); unsafe {
			return fmt.Errorf("refusing to snapshot: %s — a checkpoint of it would hash far beyond any project; run @coder from a project directory", reason)
		}
		if err := snapshotWorkspace(opCtx, root, *label); err != nil {
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
		out, err := shadowGit(opCtx, gitDir, root, "log", fmt.Sprintf("--max-count=%d", checkpointMaxList),
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
