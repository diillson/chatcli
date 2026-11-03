/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// /restart: close this session the same way /exit does, then start the
// chatcli binary that is installed NOW — the one /update or the auto-update
// staging just put on disk — with the same arguments, and resume the same
// conversation in it.
//
// The request travels in two steps. The handler only records a plan and
// unwinds the REPL like /exit, so Start's deferred cleanup runs untouched
// (hooks, history, MCP teardown, cost snapshot, autosave). main defers
// ExecPendingRestart as its very first statement, so it runs after every
// other defer of the process (headless browser, logger sync) and replaces
// the process last. The conversation is handed over through one internal
// environment variable that the new process consumes and clears at boot.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/version"
	"go.uber.org/zap"
)

// restartHandoffEnv carries the restart handoff (JSON restartHandoff) from
// the old process to the new one. Internal: set only by /restart and cleared
// by the new process as soon as it reads it, so nothing it spawns inherits it.
const restartHandoffEnv = "CHATCLI_RESTART_HANDOFF"

// restartHandoff is what the new process needs to continue where the old one
// stopped: the session to load and the version it restarted from.
type restartHandoff struct {
	Session string `json:"session,omitempty"`
	From    string `json:"from,omitempty"`
}

// restartPlan is the request recorded by /restart and executed by
// ExecPendingRestart once the process has shut down cleanly.
type restartPlan struct {
	handoff restartHandoff
}

var (
	pendingRestartMu sync.Mutex
	pendingRestart   *restartPlan

	// launchDir is the working directory the process started in: a relative
	// argv[0] ("./chatcli") is resolved against it, not against wherever the
	// session moved since.
	launchDir, _ = os.Getwd()

	// Seams for the tests: no test ever replaces the test binary.
	restartLookPath   = exec.LookPath
	restartExecutable = os.Executable
	restartExecFn     = execRestart
	restartArgs       = func() []string { return os.Args }
)

// handleRestartCommand handles /restart. It returns true (leave the REPL)
// when a restart was scheduled.
func (cli *ChatCLI) handleRestartCommand(_ context.Context, _ string) bool {
	// Only the interactive terminal can be restarted: a scheduler job, the
	// gateway or an ACP/web client drive this handler without a REPL to
	// unwind, and replacing a server process from a chat line is not a
	// command's job.
	if !cli.replActive {
		fmt.Println(colorize("  "+i18n.T("restart.not_interactive"), ColorYellow))
		return false
	}
	session := cli.persistSessionForRestart()
	setPendingRestart(&restartPlan{handoff: restartHandoff{
		Session: session,
		From:    version.Version,
	}})
	if session != "" {
		fmt.Println(colorize("  "+i18n.T("restart.saving", session), ColorGray))
	}
	fmt.Println(colorize("  "+i18n.T("restart.restarting"), ColorCyan))
	return true
}

// persistSessionForRestart makes sure the conversation is on disk under a
// name the new process can load, and returns that name ("" = nothing to
// resume). The exit cleanup still runs afterwards; marking the session as
// autosaved keeps it from writing a second copy.
func (cli *ChatCLI) persistSessionForRestart() string {
	// A hub-synced session is resumed by the hub itself on the next start.
	if cli.hubSync != nil || cli.sessionManager == nil {
		return ""
	}
	name := cli.currentSessionName
	// Remote-only storage was the user's explicit choice: the remote copy is
	// the one to load, a local file would defeat it.
	if name != "" && cli.boundRemoteOnly {
		return name
	}
	if name == "" {
		if !sessionAutosaveEnabled() || cli.nonSystemMessageCount() == 0 {
			return ""
		}
		name = autosavePrefix + time.Now().Format("20060102-150405")
	}
	if err := cli.sessionManager.SaveSessionV2(name, cli.buildSessionData()); err != nil {
		cli.logger.Warn("restart: session save failed", zap.String("session", name), zap.Error(err))
		return ""
	}
	cli.sessionAutosaved = true
	return name
}

// nonSystemMessageCount counts the turns of the conversation (system
// messages are scaffolding, not conversation).
func (cli *ChatCLI) nonSystemMessageCount() int {
	n := 0
	for _, m := range cli.history {
		if m.Role != "system" {
			n++
		}
	}
	return n
}

func setPendingRestart(p *restartPlan) {
	pendingRestartMu.Lock()
	pendingRestart = p
	pendingRestartMu.Unlock()
}

func takePendingRestart() *restartPlan {
	pendingRestartMu.Lock()
	defer pendingRestartMu.Unlock()
	p := pendingRestart
	pendingRestart = nil
	return p
}

// ExecPendingRestart starts the installed chatcli in place of this process
// when /restart asked for it, and is a no-op otherwise. main defers it first,
// so it runs after every other deferred shutdown step. It returns only when
// no restart was pending or the new process could not be started (the
// failure is reported with how to start it by hand).
func ExecPendingRestart() {
	plan := takePendingRestart()
	if plan == nil {
		return
	}
	args := restartArgs()
	argv0 := ""
	if len(args) > 0 {
		argv0 = args[0]
	}
	bin, err := resolveRestartBinary(argv0)
	if err == nil {
		err = restartExecFn(bin, args, restartEnv(os.Environ(), plan.handoff))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.T("restart.failed", err))
		if plan.handoff.Session != "" {
			fmt.Fprintln(os.Stderr, i18n.T("restart.failed_resume_hint", plan.handoff.Session))
		}
	}
}

// resolveRestartBinary finds the binary to start: the one the user's command
// resolves to now, not the file this process was loaded from. On Homebrew
// the running file lives in a versioned Cellar directory the upgrade
// replaced; the PATH entry (a symlink) already points to the new version. A
// self-replaced release binary and a go install keep the same path.
func resolveRestartBinary(argv0 string) (string, error) {
	if argv0 != "" {
		candidate := argv0
		if strings.ContainsAny(argv0, `/\`) {
			if !filepath.IsAbs(candidate) && launchDir != "" {
				candidate = filepath.Join(launchDir, candidate)
			}
			if isRegularFile(candidate) {
				return candidate, nil
			}
		} else if p, err := restartLookPath(argv0); err == nil {
			return p, nil
		}
	}
	exe, err := restartExecutable()
	if err != nil {
		return "", err
	}
	if !isRegularFile(exe) {
		return "", fmt.Errorf("%s: %w", exe, os.ErrNotExist)
	}
	return exe, nil
}

// isRegularFile reports whether path (symlinks followed) is a regular file.
func isRegularFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// restartEnv is the environment of the new process: this one's, with the
// handoff set (replacing a stale value, never appending a duplicate).
func restartEnv(base []string, h restartHandoff) []string {
	out := make([]string, 0, len(base)+1)
	prefix := restartHandoffEnv + "="
	for _, kv := range base {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	raw, err := json.Marshal(h)
	if err != nil {
		return out
	}
	return append(out, prefix+string(raw))
}

// consumeRestartHandoff reads and clears the handoff a /restart left for
// this process. ok is false on a normal start.
func consumeRestartHandoff() (restartHandoff, bool) {
	raw, present := os.LookupEnv(restartHandoffEnv)
	if !present {
		return restartHandoff{}, false
	}
	_ = os.Unsetenv(restartHandoffEnv)
	var h restartHandoff
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return restartHandoff{}, false
	}
	return h, true
}

// resumeAfterRestart runs at boot: when this process was started by
// /restart it says so and reloads the conversation. It reports whether a
// handoff was found, so the boot can skip the "last session" hint that
// would point at the very session just resumed.
func (cli *ChatCLI) resumeAfterRestart(ctx context.Context) bool {
	h, ok := consumeRestartHandoff()
	if !ok {
		return false
	}
	switch {
	case h.From != "" && h.From != version.Version:
		fmt.Println(colorize("  "+i18n.T("restart.done_upgraded", h.From, version.Version), ColorLime))
	default:
		fmt.Println(colorize("  "+i18n.T("restart.done"), ColorLime))
	}
	if h.Session != "" && cli.sessionManager != nil && cli.hubSync == nil {
		cli.handleLoadSession(ctx, h.Session)
	}
	return true
}
