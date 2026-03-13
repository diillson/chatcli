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
)

const (
	// Version is the @coder plugin version.
	Version = "2.0.0"

	// DefaultMaxBytes is the default byte limit for file reads.
	DefaultMaxBytes = 200_000

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

// systemBinPaths are allowed for read/execute operations.
var systemBinPaths = []string{
	"/usr/bin/", "/usr/local/bin/", "/bin/", "/usr/sbin/",
	"/opt/homebrew/bin/",
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

// validatePath checks that a file path is within the workspace boundary and not sensitive.
func (e *Engine) validatePath(target string) error {
	if target == "" {
		return nil
	}

	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("cannot resolve path %q: %w", target, err)
	}

	// Resolve symlinks (follow the real path)
	resolved := abs
	if evalPath, err := filepath.EvalSymlinks(abs); err == nil {
		resolved = evalPath
	} else {
		parent := filepath.Dir(abs)
		if evalParent, err2 := filepath.EvalSymlinks(parent); err2 == nil {
			resolved = filepath.Join(evalParent, filepath.Base(abs))
		}
	}

	// Block sensitive system paths
	for _, sp := range sensitivePaths {
		if strings.HasPrefix(resolved, sp) {
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
		for _, bp := range systemBinPaths {
			if strings.HasPrefix(resolved, bp) {
				isSystemBin = true
				break
			}
		}

		if !isSystemBin && resolved != boundary && !strings.HasPrefix(resolved, boundary+"/") {
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
		return e.handleWrite(args)
	case "patch":
		return e.handlePatch(args)
	case "tree":
		return e.handleTree(args)
	case "search":
		return e.handleSearch(ctx, args)
	case "exec":
		return e.handleExec(ctx, args)
	case "rollback":
		return e.handleRollback(args)
	case "clean":
		return e.handleClean(args)
	case "git-status":
		return e.handleGitStatus(args)
	case "git-diff":
		return e.handleGitDiff(args)
	case "git-log":
		return e.handleGitLog(args)
	case "git-changed":
		return e.handleGitChanged(args)
	case "git-branch":
		return e.handleGitBranch(args)
	case "test":
		return e.handleTest(ctx, args)
	default:
		return fmt.Errorf("comando desconhecido: %s", cmd)
	}
}

func (e *Engine) printf(format string, a ...interface{}) {
	fmt.Fprintf(e.Out, format, a...)
}

func (e *Engine) println(a ...interface{}) {
	fmt.Fprintln(e.Out, a...)
}

func (e *Engine) errorf(format string, a ...interface{}) {
	fmt.Fprintf(e.Err, format, a...)
}

func (e *Engine) printCommandOutput(out string, err error) error {
	if strings.TrimSpace(out) != "" {
		e.println(strings.TrimRight(out, "\n"))
	}
	if err != nil {
		e.printf("❌ Falhou: %v\n", err)
		return fmt.Errorf("command failed: %v", err)
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
