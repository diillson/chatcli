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

	"github.com/diillson/chatcli/cli/workspace"
	"go.uber.org/zap"
)

func TestMemoryExportImportRecallWhy_Commands(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_AUTORECALL", "true")
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := &ChatCLI{logger: zap.NewNop(), costTracker: NewCostTracker(), memoryStore: workspace.NewMemoryStore(filepath.Join(home, "a"), zap.NewNop())}
	src.memoryStore.Manager().Facts.AddFactWithMeta("The deploy freeze ends on Friday after the release train", "project", []string{"deploy", "freeze"}, "/work/other", 0.9, "user")

	// Default export path lands under ~/.chatcli/exports.
	src.handleMemoryCommand(context.Background(), "/memory export")
	entries, err := os.ReadDir(filepath.Join(home, ".chatcli", "exports"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("default export: err=%v entries=%v", err, entries)
	}
	exported := filepath.Join(home, ".chatcli", "exports", entries[0].Name())

	dst := &ChatCLI{logger: zap.NewNop(), costTracker: NewCostTracker(), memoryStore: workspace.NewMemoryStore(filepath.Join(home, "b"), zap.NewNop())}
	dst.handleMemoryCommand(context.Background(), "/memory import "+exported)
	if dst.memoryStore.Manager().Facts.Count() != 1 {
		t.Fatal("import must land the fact")
	}
	dst.handleMemoryCommand(context.Background(), "/memory import") // usage, no panic
	dst.handleMemoryCommand(context.Background(), "/memory import /nonexistent/file.jsonl")

	// Dry-run recall explains; auto-recall records the trace for /memory why.
	dst.handleMemoryCommand(context.Background(), "/memory recall deploy freeze")
	dst.handleMemoryCommand(context.Background(), "/memory why") // nothing yet
	if dst.lastRecallTrace != nil {
		t.Fatal("a dry run must not record the trace")
	}
	block := dst.memoryAutoRecallBlockCtx(context.Background(), []string{"deploy", "freeze"}, "when does the deploy freeze end")
	if !strings.Contains(block, "deploy freeze") || !strings.Contains(block, "(from: other)") {
		t.Fatalf("auto-recall block must carry the fact with its project label: %q", block)
	}
	if dst.lastRecallTrace == nil || len(dst.lastRecallTrace.Facts) != 1 || dst.lastRecallTrace.Facts[0].Why() == "" {
		t.Fatalf("auto-recall must record why: %+v", dst.lastRecallTrace)
	}
	dst.handleMemoryCommand(context.Background(), "/memory why")
}
