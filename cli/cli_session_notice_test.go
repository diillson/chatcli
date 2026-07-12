package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/ui/theme"
	"go.uber.org/zap"
)

// captureSessionStdout is a local stdout capture for the session tests.
func captureSessionStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// newSessionTestCLI builds a ChatCLI whose session manager persists into a
// throwaway directory, isolating the tests from ~/.chatcli.
func newSessionTestCLI(t *testing.T) *ChatCLI {
	t.Helper()
	theme.SetProfile(theme.ProfileNoTTY)
	t.Cleanup(func() { theme.SetProfile(theme.DetectProfile()) })
	return &ChatCLI{
		logger:         zap.NewNop(),
		sessionManager: &SessionManager{sessionsDir: t.TempDir(), logger: zap.NewNop()},
		history:        []models.Message{{Role: "user", Content: "olá"}},
	}
}

// TestSessionSaveLoadDeleteNotices drives the local save → load → delete
// round trip and asserts each step reports through the kit notices (glyph
// prefix on the grid, no baked-in emoji).
func TestSessionSaveLoadDeleteNotices(t *testing.T) {
	c := newSessionTestCLI(t)
	ctx := context.Background()

	saved := captureSessionStdout(t, func() { c.handleSaveSession(ctx, "regressao") })
	if !strings.Contains(saved, "regressao") || !strings.HasPrefix(strings.TrimLeft(saved, "\n"), "  ") {
		t.Fatalf("save notice off-grid: %q", saved)
	}
	if c.currentSessionName != "regressao" {
		t.Fatalf("session name not recorded: %q", c.currentSessionName)
	}

	c.history = nil
	loaded := captureSessionStdout(t, func() { c.handleLoadSession(ctx, "regressao") })
	if !strings.Contains(loaded, "regressao") {
		t.Fatalf("load notice missing: %q", loaded)
	}
	if len(c.history) != 1 {
		t.Fatalf("history not restored: %d", len(c.history))
	}

	deleted := captureSessionStdout(t, func() { c.handleDeleteSession(ctx, "regressao") })
	if !strings.Contains(deleted, "regressao") {
		t.Fatalf("delete notice missing: %q", deleted)
	}
	for name, out := range map[string]string{"save": saved, "load": loaded, "delete": deleted} {
		if strings.Contains(out, "✅") || strings.Contains(out, "❌") {
			t.Errorf("%s notice still carries baked-in emoji: %q", name, out)
		}
	}
}

// TestSessionLoadErrorNotice covers the error branch: loading a session
// that does not exist must produce the error notice, not a bare dump.
func TestSessionLoadErrorNotice(t *testing.T) {
	c := newSessionTestCLI(t)
	out := captureSessionStdout(t, func() { c.handleLoadSession(context.Background(), "nao-existe") })
	if strings.TrimSpace(out) == "" {
		t.Fatal("error notice missing")
	}
	if strings.Contains(out, "❌") {
		t.Errorf("error notice still carries baked-in emoji: %q", out)
	}
}

// TestSessionSaveErrorNotice drives the local save failure via an invalid
// session name (path separators are rejected by validation).
func TestSessionSaveErrorNotice(t *testing.T) {
	c := newSessionTestCLI(t)
	out := captureSessionStdout(t, func() { c.handleSaveSession(context.Background(), "in/valid") })
	if strings.TrimSpace(out) == "" {
		t.Fatal("save error notice missing")
	}
}

// TestSessionListErrorNotice drives the local list failure by pointing the
// manager at a path that is a file, not a directory.
func TestSessionListErrorNotice(t *testing.T) {
	c := newSessionTestCLI(t)
	f, err := os.CreateTemp(t.TempDir(), "not-a-dir")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	c.sessionManager.sessionsDir = f.Name()
	out := captureSessionStdout(t, func() { c.handleListSessions(context.Background()) })
	if strings.TrimSpace(out) == "" {
		t.Fatal("list error notice missing")
	}
}

// TestSessionRemoteGuardNotices covers the "remote mode without a remote
// client" guards: each handler must fail fast with the error notice.
func TestSessionRemoteGuardNotices(t *testing.T) {
	c := newSessionTestCLI(t)
	c.isRemote = true
	c.Client = &fakeClient{provider: "P", model: "m"} // not a *remote.Client
	ctx := context.Background()

	for name, fn := range map[string]func(){
		"save":   func() { c.handleSaveSession(ctx, "x") },
		"load":   func() { c.handleLoadSession(ctx, "x") },
		"delete": func() { c.handleDeleteSession(ctx, "x") },
	} {
		out := captureSessionStdout(t, fn)
		if strings.TrimSpace(out) == "" {
			t.Errorf("%s remote guard notice missing", name)
		}
	}
}
