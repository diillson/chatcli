package engine

import (
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Planting an executable on the user's PATH is the one escape that
// outlives the run, and it was the only out-of-workspace path the write
// check accepted.
func TestWriteRefusesSystemBinaryDirectories(t *testing.T) {
	e := NewEngine(io.Discard, io.Discard, t.TempDir())
	for _, p := range []string{
		"/usr/local/bin/chatcli-payload",
		"/usr/bin/chatcli-payload",
		"/bin/chatcli-payload",
		"/usr/sbin/chatcli-payload",
		"/opt/homebrew/bin/chatcli-payload",
	} {
		if err := e.validateWritePath(p); err == nil {
			t.Errorf("writing %s must be refused: it is outside the workspace", p)
		}
	}
}

// Reading and executing from those directories is legitimate — a run
// reads an interpreter or runs a tool that lives outside the workspace.
func TestReadStillAllowsSystemBinaryDirectories(t *testing.T) {
	e := NewEngine(io.Discard, io.Discard, t.TempDir())
	if err := e.validatePath("/usr/bin/env"); err != nil {
		t.Errorf("reading a system binary must stay allowed: %v", err)
	}
}

func TestWriteInsideTheWorkspaceIsUnaffected(t *testing.T) {
	ws := t.TempDir()
	e := NewEngine(io.Discard, io.Discard, ws)
	if err := e.validateWritePath(filepath.Join(ws, "pkg", "main.go")); err != nil {
		t.Errorf("a workspace write must pass: %v", err)
	}
}

// The denylist is compared against a path already resolved through
// EvalSymlinks. On macOS /etc resolves to /private/etc, so the literal
// entries matched nothing and the list was inert on that platform.
func TestSensitivePathsResolveThroughSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix denylist")
	}
	e := NewEngine(io.Discard, io.Discard, "")
	for _, p := range []string{"/etc/passwd", "/etc/ssh/sshd_config"} {
		err := e.validatePath(p)
		if err == nil {
			t.Errorf("%s must be refused", p)
			continue
		}
		if !strings.Contains(err.Error(), "sensitive") {
			t.Errorf("%s must be refused as sensitive, got %v", p, err)
		}
	}
}

func TestResolveDenyListKeepsBothSpellings(t *testing.T) {
	got := resolveDenyList([]string{"/etc/ssh/"})
	if len(got) == 0 || got[0] != "/etc/ssh/" {
		t.Fatalf("the literal entry must survive: %+v", got)
	}
	if runtime.GOOS == "darwin" && len(got) != 2 {
		t.Errorf("darwin must also carry the resolved spelling: %+v", got)
	}
	for _, entry := range got {
		if !strings.HasSuffix(entry, "/") {
			t.Errorf("a directory entry must keep its separator: %q", entry)
		}
	}
}

// Writing a new file into a new subdirectory leaves both missing, so
// neither resolves — and on a system where the workspace sits under a
// symlink the unresolved path read as an escape from the resolved
// boundary.
func TestWriteIntoANewSubdirectoryOfASymlinkedWorkspace(t *testing.T) {
	ws := t.TempDir()
	e := NewEngine(io.Discard, io.Discard, ws)
	deep := filepath.Join(ws, "a", "b", "c", "new.go")
	if err := e.validateWritePath(deep); err != nil {
		t.Errorf("a write into a new subtree of the workspace must pass: %v", err)
	}
}

func TestResolveThroughExistingAncestorFallsBackToTheInput(t *testing.T) {
	// Nothing on this path exists; the cleaned absolute path is the best
	// answer and must be returned rather than an empty string.
	got := resolveThroughExistingAncestor(filepath.Join(string(filepath.Separator),
		"chatcli-nonexistent-root", "x", "y"))
	if got == "" {
		t.Fatal("resolution must never return empty")
	}
	if !filepath.IsAbs(got) {
		t.Errorf("resolution must stay absolute, got %q", got)
	}
}
