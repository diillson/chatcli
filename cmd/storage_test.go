/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// TestRunStorageWith covers the boot-free subcommand body: inventory,
// JSON output, the dry-run default, apply only with prune, unknown stores.
func TestRunStorageWith(t *testing.T) {
	i18n.Init()
	ctx := context.Background()
	root := t.TempDir()
	old := time.Now().Add(-400 * 24 * time.Hour)
	p := filepath.Join(root, "sessions", "autosave-old.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}

	t.Run("inventory as json", func(t *testing.T) {
		var sb strings.Builder
		if err := runStorageWith(ctx, []string{"--json"}, root, zap.NewNop(), &sb); err != nil {
			t.Fatalf("json: %v", err)
		}
		var res cli.StorageResult
		if err := json.Unmarshal([]byte(sb.String()), &res); err != nil {
			t.Fatalf("not json: %v\n%s", err, sb.String())
		}
		if res.Applied || res.Prunable != 1 {
			t.Fatalf("want a dry inventory with one prunable session, got applied=%v prunable=%d", res.Applied, res.Prunable)
		}
	})

	t.Run("prune is a dry run unless --apply", func(t *testing.T) {
		var sb strings.Builder
		if err := runStorageWith(ctx, []string{"prune", "sessions"}, root, zap.NewNop(), &sb); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(p); err != nil {
			t.Fatal("dry run removed the session")
		}
		if !strings.Contains(sb.String(), "--apply") {
			t.Fatalf("dry run must say how to apply: %s", sb.String())
		}
	})

	t.Run("--apply without prune is refused", func(t *testing.T) {
		var sb strings.Builder
		if err := runStorageWith(ctx, []string{"--apply"}, root, zap.NewNop(), &sb); err == nil {
			t.Fatal("apply without prune must be refused")
		}
	})

	t.Run("unknown store", func(t *testing.T) {
		var sb strings.Builder
		if err := runStorageWith(ctx, []string{"prune", "bogus"}, root, zap.NewNop(), &sb); err == nil || !strings.Contains(err.Error(), "bogus") {
			t.Fatalf("want an error naming the store, got %v", err)
		}
	})

	t.Run("prune --apply removes", func(t *testing.T) {
		var sb strings.Builder
		if err := runStorageWith(ctx, []string{"prune", "sessions", "--apply"}, root, zap.NewNop(), &sb); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("apply did not remove the expired machine session")
		}
	})
}
