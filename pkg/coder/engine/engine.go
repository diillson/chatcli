package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/diillson/chatcli/pkg/fspath"
)

const (
	// Version is the @coder plugin version.
	Version = "2.0.0"

	// DefaultMaxBytes is the default byte limit for file reads.
	DefaultMaxBytes = 200_000

	// DefaultMaxLines caps a read that asked for no range (the byte cap
	// stays the ceiling); the model pages with --start/--end.
	DefaultMaxLines = 2_000

	// DefaultMaxEntries is the default entry limit for tree listings.
	DefaultMaxEntries = 2_000
)

// Engine is the core execution engine for @coder commands.
// It is stdlib-only and writes all output to the provided io.Writer instances.
type Engine struct {
	Out           io.Writer // primary output (replaces os.Stdout)
	Err           io.Writer // error/debug output (replaces os.Stderr)
	WorkspaceRoot string    // workspace boundary for path validation (empty = cwd)
}

// sensitivePaths are system paths that must never be written to.
var sensitivePaths = []string{
	"/etc/passwd", "/etc/shadow", "/etc/sudoers",
	"/etc/ssh/", "/etc/ssl/",
	"/proc/", "/sys/", "/dev/",
	"/boot/", "/sbin/",
}

// auxAllowedPaths is a package-level registry of directories that the engine
// treats as inside the boundary for read/write/exec, in addition to the
// configured WorkspaceRoot. Callers (e.g. the CLI session bootstrap) register
// paths like the session scratch dir and the tool-result overflow dir.
//
// This avoids threading an allowlist through every NewEngine() call site
// (there are 25+) while keeping the security model explicit: only code in
// the same process can extend the allowlist — untrusted input never touches
// these functions.
var (
	auxAllowedPaths   []string
	auxAllowedPathsMu sync.RWMutex
)

// RegisterAuxPath adds path to the aux allowlist. No-op if already registered
// or path is empty. The path is resolved to its absolute form; symlinks are
// resolved on first use by validatePath.
func RegisterAuxPath(path string) {
	if path == "" {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return
	}
	auxAllowedPathsMu.Lock()
	defer auxAllowedPathsMu.Unlock()
	for _, existing := range auxAllowedPaths {
		if existing == abs {
			return
		}
	}
	auxAllowedPaths = append(auxAllowedPaths, abs)
}

// UnregisterAuxPath removes path from the aux allowlist.
func UnregisterAuxPath(path string) {
	if path == "" {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return
	}
	auxAllowedPathsMu.Lock()
	defer auxAllowedPathsMu.Unlock()
	filtered := auxAllowedPaths[:0]
	for _, existing := range auxAllowedPaths {
		if existing != abs {
			filtered = append(filtered, existing)
		}
	}
	auxAllowedPaths = filtered
}

// auxAllowedPathsSnapshot returns a copy for safe iteration during validation.
func auxAllowedPathsSnapshot() []string {
	auxAllowedPathsMu.RLock()
	defer auxAllowedPathsMu.RUnlock()
	if len(auxAllowedPaths) == 0 {
		return nil
	}
	out := make([]string, len(auxAllowedPaths))
	copy(out, auxAllowedPaths)
	return out
}

// systemBinPaths are allowed for READ and EXECUTE only. A run needs to
// read an interpreter or run a tool that lives outside the workspace;
// nothing legitimate needs to WRITE there, and treating the two the same
// let a write escape the workspace boundary entirely — planting an
// executable on the user's PATH is the one escape that outlives the run.
var systemBinPaths = []string{
	"/usr/bin/", "/usr/local/bin/", "/bin/", "/usr/sbin/",
	"/opt/homebrew/bin/",
}

// resolvedSensitivePaths is sensitivePaths with every entry put through
// the same symlink resolution the candidate goes through.
//
// The list is compared against a path already resolved by EvalSymlinks,
// and on macOS /etc resolves to /private/etc — so a literal "/etc/" entry
// matched nothing and the whole denylist was inert on that platform.
// Resolved once at init because the mapping is a property of the system,
// not of the path being checked.
var resolvedSensitivePaths = resolveDenyList(sensitivePaths)

func resolveDenyList(paths []string) []string {
	out := make([]string, 0, len(paths)*2)
	for _, p := range paths {
		out = append(out, p)
		trimmed := strings.TrimSuffix(p, "/")
		resolved, err := filepath.EvalSymlinks(trimmed)
		if err != nil || resolved == trimmed {
			continue
		}
		if strings.HasSuffix(p, "/") {
			resolved += "/"
		}
		out = append(out, resolved)
	}
	return out
}

// NewEngine creates an Engine that writes to the given writers.
// workspaceRoot defines the boundary for path validation; empty defaults to cwd.
func NewEngine(out, errOut io.Writer, workspaceRoot string) *Engine {
	root := workspaceRoot
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		}
	}
	return &Engine{Out: out, Err: errOut, WorkspaceRoot: root}
}

// resolveThroughExistingAncestor resolves the symlinks of a path that may
// not exist yet.
//
// EvalSymlinks fails on a missing path, and resolving only the immediate
// parent is not enough: writing a new file into a new subdirectory leaves
// both missing, so the path stayed unresolved while the boundary it is
// compared against was resolved — and on any system where the workspace
// sits under a symlink (macOS puts temporary and user directories under
// one) a legitimate write read as an escape. So it walks up to the
// nearest ancestor that does exist, resolves that, and rejoins the rest.
func resolveThroughExistingAncestor(abs string) string {
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	dir, rest := filepath.Dir(abs), filepath.Base(abs)
	for {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the root without finding anything that exists:
			// the cleaned absolute path is the best answer available.
			return abs
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// validatePath checks that a path is within the workspace boundary and not
// sensitive, for reading or executing. System binary directories pass:
// a run legitimately reads an interpreter or runs a tool from outside the
// workspace.
func (e *Engine) validatePath(target string) error {
	return e.validatePathForAccess(target, false)
}

// validateWritePath is validatePath for a path about to be written. It
// refuses the system binary directories: the workspace boundary is the
// promise this tool makes, and a write outside it is the one effect that
// outlives the run.
func (e *Engine) validateWritePath(target string) error {
	return e.validatePathForAccess(target, true)
}

func (e *Engine) validatePathForAccess(target string, write bool) error {
	if target == "" {
		return nil
	}

	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("cannot resolve path %q: %w", target, err)
	}

	resolved := resolveThroughExistingAncestor(abs)

	// Block sensitive system paths
	for _, sp := range resolvedSensitivePaths {
		if strings.HasPrefix(resolved, sp) {
			return fmt.Errorf("access to sensitive path %q is blocked", target)
		}
	}
	for _, sp := range fspath.WindowsSystemRoots() {
		if fspath.WithinBoundary(resolved, sp) {
			return fmt.Errorf("access to sensitive path %q is blocked", target)
		}
	}

	// Enforce workspace boundary
	if e.WorkspaceRoot != "" {
		boundary, err := filepath.Abs(e.WorkspaceRoot)
		if err == nil {
			if evalB, err2 := filepath.EvalSymlinks(boundary); err2 == nil {
				boundary = evalB
			}
		}

		isSystemBin := false
		if !write {
			for _, bp := range systemBinPaths {
				if strings.HasPrefix(resolved, bp) {
					isSystemBin = true
					break
				}
			}
		}

		if !isSystemBin && !fspath.WithinBoundary(resolved, boundary) {
			// Check aux allowlist (e.g. session scratch dir, tool-result
			// overflow). These are registered only by the CLI itself, so
			// they're trusted.
			for _, aux := range auxAllowedPathsSnapshot() {
				if evalAux, err := filepath.EvalSymlinks(aux); err == nil {
					aux = evalAux
				}
				if fspath.WithinBoundary(resolved, aux) {
					return nil
				}
			}
			return fmt.Errorf("path %q is outside workspace boundary %q", target, e.WorkspaceRoot)
		}
	}

	return nil
}

// Execute dispatches a subcommand with the given args.
func (e *Engine) Execute(ctx context.Context, cmd string, args []string) error {
	switch cmd {
	case "read":
		return e.handleRead(args)
	case "write":
		e.autoCheckpoint(ctx, "write")
		return e.handleWrite(args)
	case "patch":
		e.autoCheckpoint(ctx, "patch")
		return e.handlePatch(args)
	case "multipatch":
		e.autoCheckpoint(ctx, "multipatch")
		return e.handleMultipatch(args)
	case "tree":
		return e.handleTree(args)
	case "checkpoint":
		return e.handleCheckpoint(ctx, args)
	case "outline":
		return e.handleOutline(args)
	case "map":
		return e.handleMap(args)
	case "search":
		return e.handleSearch(ctx, args)
	case "exec":
		e.autoCheckpoint(ctx, "exec")
		return e.handleExec(ctx, args)
	case "rollback":
		return e.handleRollback(args)
	case "clean":
		return e.handleClean(args)
	case "git-status":
		return e.handleGitStatus(ctx, args)
	case "git-diff":
		return e.handleGitDiff(ctx, args)
	case "git-log":
		return e.handleGitLog(ctx, args)
	case "git-changed":
		return e.handleGitChanged(ctx, args)
	case "git-branch":
		return e.handleGitBranch(ctx, args)
	case "test":
		return e.handleTest(ctx, args)
	default:
		return fmt.Errorf("comando desconhecido: %s", cmd)
	}
}

func (e *Engine) printf(format string, a ...interface{}) {
	_, _ = fmt.Fprintf(e.Out, format, a...)
}

func (e *Engine) println(a ...interface{}) {
	_, _ = fmt.Fprintln(e.Out, a...)
}

func (e *Engine) errorf(format string, a ...interface{}) {
	_, _ = fmt.Fprintf(e.Err, format, a...)
}

func (e *Engine) printCommandOutput(out string, err error) error {
	if strings.TrimSpace(out) != "" {
		e.println(strings.TrimRight(out, "\n"))
	}
	if err != nil {
		e.printf("❌ Falhou: %v\n", err)
		return fmt.Errorf("command failed: %w", err)
	}
	return nil
}

// StreamWriter implements io.Writer and calls onOutput per complete line.
// Partial lines are buffered until a newline arrives or Flush() is called.
type StreamWriter struct {
	onOutput func(string)
	buf      []byte
	mu       sync.Mutex
}

// NewStreamWriter creates a StreamWriter that calls onOutput for each line.
func NewStreamWriter(onOutput func(string)) *StreamWriter {
	return &StreamWriter{onOutput: onOutput}
}

func (sw *StreamWriter) Write(p []byte) (int, error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	sw.buf = append(sw.buf, p...)
	for {
		idx := bytes.IndexByte(sw.buf, '\n')
		if idx < 0 {
			break
		}
		line := string(sw.buf[:idx])
		line = strings.TrimSuffix(line, "\r")
		if sw.onOutput != nil {
			sw.onOutput(line)
		}
		sw.buf = sw.buf[idx+1:]
	}
	// Flush oversized buffers to avoid unbounded memory
	if len(sw.buf) > 4096 {
		if sw.onOutput != nil {
			sw.onOutput(string(sw.buf))
		}
		sw.buf = sw.buf[:0]
	}
	return len(p), nil
}

// Flush emits any remaining buffered content as a final line.
func (sw *StreamWriter) Flush() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if len(sw.buf) > 0 && sw.onOutput != nil {
		sw.onOutput(string(sw.buf))
		sw.buf = sw.buf[:0]
	}
}
