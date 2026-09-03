/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"go.uber.org/zap"
)

func newRefreshHandler(t *testing.T) (*ContextHandler, string) {
	t.Helper()
	h, err := NewContextHandlerAt(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "doc.md"), []byte("# doc\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.manager.CreateContext(context.Background(), "notes", "", []string{src}, ctxmgr.ModeFull, nil, false); err != nil {
		t.Fatal(err)
	}
	return h, src
}

func TestContextRefreshCommand_UpToDateThenChanged(t *testing.T) {
	h, src := newRefreshHandler(t)
	ctx := context.Background()
	if err := h.HandleContextCommand(ctx, "s", "/context refresh notes"); err != nil {
		t.Fatalf("refresh up to date: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "doc.md"), []byte("# doc\nchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleContextCommand(ctx, "s", "/context reindex notes"); err != nil {
		t.Fatalf("refresh after edit: %v", err)
	}
	if err := h.HandleContextCommand(ctx, "s", "/context refresh"); err == nil || !strings.Contains(err.Error(), "context.refresh.usage") && !strings.Contains(strings.ToLower(err.Error()), "usage") {
		t.Fatalf("missing name must show usage, got %v", err)
	}
	if err := h.HandleContextCommand(ctx, "s", "/context refresh nope"); err == nil {
		t.Fatal("unknown context must error")
	}
	// Legacy context without sources.
	h.manager.RegisterLegacyForTest("legacy")
	if err := h.HandleContextCommand(ctx, "s", "/context refresh legacy"); err == nil {
		t.Fatal("legacy context must explain how to enable refresh")
	}
}

func TestContextWatchCommand_Lifecycle(t *testing.T) {
	h, _ := newRefreshHandler(t)
	defer h.Close()
	var notices []string
	h.SetRefreshNotifier(func(s string) { notices = append(notices, s) })
	ctx := context.Background()
	if err := h.HandleContextCommand(ctx, "s", "/context watch list"); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleContextCommand(ctx, "s", "/context watch notes"); err != nil {
		t.Fatalf("watch: %v", err)
	}
	if got := h.contextWatcher().Watching(); len(got) != 1 || got[0] != "notes" {
		t.Fatalf("watching = %v", got)
	}
	if err := h.HandleContextCommand(ctx, "s", "/context watch notes off"); err != nil {
		t.Fatalf("watch off: %v", err)
	}
	if err := h.HandleContextCommand(ctx, "s", "/context unwatch notes"); err == nil {
		t.Fatal("unwatching an unwatched context must error")
	}
	if err := h.HandleContextCommand(ctx, "s", "/context watch nope"); err == nil {
		t.Fatal("unknown context must error")
	}
	if err := h.HandleContextCommand(ctx, "s", "/context unwatch"); err == nil {
		t.Fatal("unwatch without a name must show usage")
	}
	_ = notices
}
