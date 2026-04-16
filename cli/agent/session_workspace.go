/*
 * ChatCLI - Session Workspace
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Per-session scratch directory that the agent can read/write freely without
 * touching the project tree. Created once per CLI startup, cleaned up on exit.
 *
 * Layout:
 *   $TMPDIR/chatcli-agent-<random>/
 *     ├── scratch/        -> agent-writable: temp scripts, intermediate files
 *     └── tool-results/   -> overflow from EnforceToolResultBudget (replaces
 *                            the old global /tmp/chatcli-tool-results/)
 *
 * The session workspace registers both subdirs with:
 *   - pkg/coder/engine (write allowlist)
 *   - cli/agent.SensitiveReadPaths (read allowlist)
 *   - tool_result_budget (writes overflow here instead of the global dir)
 *
 * Env vars:
 *   CHATCLI_AGENT_TMPDIR   -> absolute path of scratch dir; exported for
 *                              child processes spawned by run_command / exec.
 *   CHATCLI_AGENT_KEEP_TMPDIR=true -> skip cleanup (debugging).
 */
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/diillson/chatcli/pkg/coder/engine"
	"go.uber.org/zap"
)

// sessionWorkspace is the package-level singleton.
var (
	sessionWorkspace   *SessionWorkspace
	sessionWorkspaceMu sync.RWMutex
)

// ResultDirSetter is a callback other packages (workers, plugins) can register
// so the session workspace can wire the overflow dir without creating import
// cycles.
type ResultDirSetter func(dir string)

var (
	resultDirSetters   []ResultDirSetter
	resultDirSettersMu sync.Mutex
)

// RegisterResultDirSetter adds a callback that will be invoked with the
// session tool-results directory during InitSessionWorkspace (and with "" on
// Cleanup).
func RegisterResultDirSetter(fn ResultDirSetter) {
	if fn == nil {
		return
	}
	resultDirSettersMu.Lock()
	resultDirSetters = append(resultDirSetters, fn)
	// If the session is already up, fire immediately.
	if sessionWorkspace != nil {
		dir := sessionWorkspace.ToolResultsDir
		resultDirSettersMu.Unlock()
		fn(dir)
		return
	}
	resultDirSettersMu.Unlock()
}

// fireResultDirSetters invokes all registered callbacks with dir.
func fireResultDirSetters(dir string) {
	resultDirSettersMu.Lock()
	setters := make([]ResultDirSetter, len(resultDirSetters))
	copy(setters, resultDirSetters)
	resultDirSettersMu.Unlock()
	for _, fn := range setters {
		fn(dir)
	}
}

// SessionWorkspace is a per-CLI-session scratch directory the agent is
// unconditionally allowed to read and write under.
type SessionWorkspace struct {
	Root           string // e.g. /tmp/chatcli-agent-abc123
	ScratchDir     string // Root/scratch  -> CHATCLI_AGENT_TMPDIR
	ToolResultsDir string // Root/tool-results

	keep   bool
	logger *zap.Logger
}

// InitSessionWorkspace creates the per-session workspace and registers its
// subdirs with the engine + read validators. Idempotent: calling it twice
// returns the existing workspace.
//
// The returned workspace should have Cleanup() called at CLI shutdown.
func InitSessionWorkspace(logger *zap.Logger) (*SessionWorkspace, error) {
	sessionWorkspaceMu.Lock()
	defer sessionWorkspaceMu.Unlock()

	if sessionWorkspace != nil {
		return sessionWorkspace, nil
	}

	root, err := os.MkdirTemp("", "chatcli-agent-")
	if err != nil {
		return nil, fmt.Errorf("creating session tmpdir: %w", err)
	}

	scratch := filepath.Join(root, "scratch")
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("creating scratch dir: %w", err)
	}

	toolResults := filepath.Join(root, "tool-results")
	if err := os.MkdirAll(toolResults, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("creating tool-results dir: %w", err)
	}

	keep := strings.EqualFold(os.Getenv("CHATCLI_AGENT_KEEP_TMPDIR"), "true")

	ws := &SessionWorkspace{
		Root:           root,
		ScratchDir:     scratch,
		ToolResultsDir: toolResults,
		keep:           keep,
		logger:         logger,
	}

	// Export env var so child processes (run_command, exec) can use it.
	_ = os.Setenv("CHATCLI_AGENT_TMPDIR", scratch)

	// Register with read validator (agent package).
	RegisterAuxReadPath(root)

	// Register with engine write/exec validator.
	engine.RegisterAuxPath(root)

	// System temp directories are allowlisted by default. Every major
	// model defaults to writing throwaway scripts under /tmp ("cat >
	// /tmp/check.sh && bash /tmp/check.sh"); blocking that forces the
	// model to guess a safe path and usually fails. The risk is low —
	// these dirs are user-owned on single-user machines. Users who
	// need strict sandboxing can set CHATCLI_BLOCK_TMP_WRITES=true.
	if !strings.EqualFold(os.Getenv("CHATCLI_BLOCK_TMP_WRITES"), "true") {
		tmpPaths := []string{os.TempDir()}
		// On macOS os.TempDir() returns a per-user /var/folders/... path,
		// but models emit /tmp verbatim. On Linux they're the same. Add
		// /tmp explicitly when it exists and differs.
		if _, err := os.Stat("/tmp"); err == nil {
			tmpPaths = append(tmpPaths, "/tmp")
		}
		seen := map[string]bool{}
		for _, p := range tmpPaths {
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			engine.RegisterAuxPath(p)
			RegisterAuxReadPath(p)
		}
		if logger != nil {
			logger.Info("System temp dirs added to read/write allowlist",
				zap.Strings("paths", tmpPaths),
				zap.String("opt_out_env", "CHATCLI_BLOCK_TMP_WRITES=true"))
		}
	}

	// Wire the tool-results dir into the budget system.
	SetBudgetResultDir(toolResults)

	sessionWorkspace = ws

	// Notify other packages (workers.SetResultDir, ...).
	fireResultDirSetters(toolResults)

	if logger != nil {
		logger.Info("Session workspace initialized",
			zap.String("root", root),
			zap.String("scratch", scratch),
			zap.String("tool_results", toolResults),
			zap.Bool("keep_on_exit", keep))
	}

	return ws, nil
}

// GetSessionWorkspace returns the active session workspace or nil if
// InitSessionWorkspace was never called.
func GetSessionWorkspace() *SessionWorkspace {
	sessionWorkspaceMu.RLock()
	defer sessionWorkspaceMu.RUnlock()
	return sessionWorkspace
}

// Cleanup removes the session workspace unless CHATCLI_AGENT_KEEP_TMPDIR=true.
// Safe to call multiple times.
func (ws *SessionWorkspace) Cleanup() {
	if ws == nil {
		return
	}

	sessionWorkspaceMu.Lock()
	defer sessionWorkspaceMu.Unlock()

	// Unregister first so a late tool call can't land a file into a
	// directory we're about to delete.
	UnregisterAuxReadPath(ws.Root)
	engine.UnregisterAuxPath(ws.Root)
	SetBudgetResultDir("") // fall back to default
	fireResultDirSetters("")

	if ws.keep {
		if ws.logger != nil {
			ws.logger.Info("Keeping session workspace (CHATCLI_AGENT_KEEP_TMPDIR=true)",
				zap.String("root", ws.Root))
		}
	} else {
		if err := os.RemoveAll(ws.Root); err != nil && ws.logger != nil {
			ws.logger.Warn("Failed to remove session workspace",
				zap.String("root", ws.Root),
				zap.Error(err))
		}
	}

	_ = os.Unsetenv("CHATCLI_AGENT_TMPDIR")

	if sessionWorkspace == ws {
		sessionWorkspace = nil
	}
}

// AuxAllowedPaths returns paths the engine and read validator should treat as
// inside the boundary, in addition to the workspace root. Includes the
// session workspace (if initialized).
func AuxAllowedPaths() []string {
	sessionWorkspaceMu.RLock()
	defer sessionWorkspaceMu.RUnlock()
	if sessionWorkspace == nil {
		return nil
	}
	return []string{sessionWorkspace.Root}
}

// ------------------------------------------------------------------
// Read validator aux path registry
// ------------------------------------------------------------------

var (
	auxReadPaths   []string
	auxReadPathsMu sync.RWMutex
)

// RegisterAuxReadPath adds a directory that SensitiveReadPaths.IsReadAllowed
// will always permit. Typically called by the session workspace.
func RegisterAuxReadPath(path string) {
	if path == "" {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return
	}
	auxReadPathsMu.Lock()
	defer auxReadPathsMu.Unlock()
	for _, existing := range auxReadPaths {
		if existing == abs {
			return
		}
	}
	auxReadPaths = append(auxReadPaths, abs)
}

// UnregisterAuxReadPath removes a previously registered aux read path.
func UnregisterAuxReadPath(path string) {
	if path == "" {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return
	}
	auxReadPathsMu.Lock()
	defer auxReadPathsMu.Unlock()
	filtered := auxReadPaths[:0]
	for _, existing := range auxReadPaths {
		if existing != abs {
			filtered = append(filtered, existing)
		}
	}
	auxReadPaths = filtered
}

// auxReadPathsSnapshot returns a copy for safe iteration.
func auxReadPathsSnapshot() []string {
	auxReadPathsMu.RLock()
	defer auxReadPathsMu.RUnlock()
	if len(auxReadPaths) == 0 {
		return nil
	}
	out := make([]string, len(auxReadPaths))
	copy(out, auxReadPaths)
	return out
}
