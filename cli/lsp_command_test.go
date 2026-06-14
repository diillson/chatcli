package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func newLSPTestCLI() *ChatCLI { return &ChatCLI{logger: zap.NewNop()} }

func TestHandleLSP_Usage(t *testing.T) {
	// No argument prints usage and returns without touching the filesystem.
	newLSPTestCLI().handleLSPCommand(context.Background(), "/lsp")
}

func TestHandleLSP_FileNotFound(t *testing.T) {
	newLSPTestCLI().handleLSPCommand(context.Background(), "/lsp /no/such/file.go")
}

func TestHandleLSP_UnsupportedExtension(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "data.unknownext")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	newLSPTestCLI().handleLSPCommand(context.Background(), "/lsp "+f)
}

func TestHandleLSP_SpawnFailure(t *testing.T) {
	// Point the Go server at a binary that does not exist so Spawn fails fast
	// and the spawn-failure branch is exercised (no real language server).
	t.Setenv("CHATCLI_LSP_GO_CMD", "chatcli-nonexistent-lsp-binary-xyz")
	dir := t.TempDir()
	f := filepath.Join(dir, "main.go")
	if err := os.WriteFile(f, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	newLSPTestCLI().handleLSPCommand(context.Background(), "/lsp "+f)
}
