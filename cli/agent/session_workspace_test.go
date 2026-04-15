package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/coder/engine"
)

func TestInitSessionWorkspace_CreatesDirs(t *testing.T) {
	// Isolate this test from any previously-init'd workspace.
	if ws := GetSessionWorkspace(); ws != nil {
		ws.Cleanup()
	}

	ws, err := InitSessionWorkspace(nil)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { ws.Cleanup() })

	if ws.Root == "" || ws.ScratchDir == "" || ws.ToolResultsDir == "" {
		t.Fatalf("expected all dirs set, got %+v", ws)
	}
	for _, p := range []string{ws.Root, ws.ScratchDir, ws.ToolResultsDir} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s should be a dir", p)
		}
	}

	if got := os.Getenv("CHATCLI_AGENT_TMPDIR"); got != ws.ScratchDir {
		t.Fatalf("CHATCLI_AGENT_TMPDIR = %q, want %q", got, ws.ScratchDir)
	}
}

func TestInitSessionWorkspace_Idempotent(t *testing.T) {
	if ws := GetSessionWorkspace(); ws != nil {
		ws.Cleanup()
	}
	ws1, err := InitSessionWorkspace(nil)
	if err != nil {
		t.Fatalf("init1: %v", err)
	}
	defer ws1.Cleanup()
	ws2, err := InitSessionWorkspace(nil)
	if err != nil {
		t.Fatalf("init2: %v", err)
	}
	if ws1 != ws2 {
		t.Fatalf("second Init should return the same singleton")
	}
}

func TestSessionWorkspace_ReadAllowlist(t *testing.T) {
	if ws := GetSessionWorkspace(); ws != nil {
		ws.Cleanup()
	}
	ws, err := InitSessionWorkspace(nil)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { ws.Cleanup() })

	testFile := filepath.Join(ws.ScratchDir, "metrics.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Use a workspace that does NOT include the session dir.
	fakeRepo := t.TempDir()
	rv := NewSensitiveReadPaths()
	allowed, reason := rv.IsReadAllowed(testFile, fakeRepo)
	if !allowed {
		t.Fatalf("session scratch file should be readable, got blocked: %s", reason)
	}
}

func TestSessionWorkspace_EngineWriteAllowlist(t *testing.T) {
	if ws := GetSessionWorkspace(); ws != nil {
		ws.Cleanup()
	}
	ws, err := InitSessionWorkspace(nil)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { ws.Cleanup() })

	fakeRepo := t.TempDir()
	var discardOut, discardErr strings.Builder
	eng := engine.NewEngine(&discardOut, &discardErr, fakeRepo)

	target := filepath.Join(ws.ScratchDir, "patch.sh")
	if err := eng.Execute(nil, "write", []string{"--file", target, "--content", "#!/bin/sh\necho hi"}); err != nil {
		t.Fatalf("write to session scratch should succeed; got %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "echo hi") {
		t.Fatalf("content mismatch: %q", data)
	}
}

func TestSessionWorkspace_CleanupRemoves(t *testing.T) {
	// Make sure we start fresh.
	if ws := GetSessionWorkspace(); ws != nil {
		ws.Cleanup()
	}
	ws, err := InitSessionWorkspace(nil)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	root := ws.Root
	ws.Cleanup()

	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("root dir should be removed after cleanup, err=%v", err)
	}
	if got := os.Getenv("CHATCLI_AGENT_TMPDIR"); got != "" {
		t.Fatalf("CHATCLI_AGENT_TMPDIR should be unset after cleanup, got %q", got)
	}
}

func TestAuxReadPathsRegistry(t *testing.T) {
	// Start clean.
	for _, p := range auxReadPathsSnapshot() {
		UnregisterAuxReadPath(p)
	}
	before := len(auxReadPathsSnapshot())

	dir := t.TempDir()
	RegisterAuxReadPath(dir)
	if len(auxReadPathsSnapshot()) != before+1 {
		t.Fatalf("register did not add: %d -> %d", before, len(auxReadPathsSnapshot()))
	}
	// Duplicate should be idempotent.
	RegisterAuxReadPath(dir)
	if len(auxReadPathsSnapshot()) != before+1 {
		t.Fatalf("duplicate register added extra entry")
	}

	UnregisterAuxReadPath(dir)
	if len(auxReadPathsSnapshot()) != before {
		t.Fatalf("unregister did not remove")
	}
}
