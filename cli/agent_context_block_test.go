/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/manager"
	"go.uber.org/zap"
)

// i18n is initialized by TestMain in config_sections_test.go.

func TestAttachedContextAgentBlock_NilHandlerIsEmpty(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop()}
	if got := c.attachedContextAgentBlock(); got != "" {
		t.Fatalf("nil contextHandler must yield empty block; got %q", got)
	}
}

// TestAttachedContextAgentBlock_SessionScoped pins the parity fix: a
// /context attachment reaches the agent/coder system prompt for ITS session
// and never leaks into another.
func TestAttachedContextAgentBlock_SessionScoped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ch, err := NewContextHandler(zap.NewNop())
	if err != nil {
		t.Fatalf("NewContextHandler: %v", err)
	}
	c := &ChatCLI{logger: zap.NewNop(), contextHandler: ch}

	dir := t.TempDir()
	f := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(f, []byte("AGENT-CONTEXT-MARKER spec body"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := ch.GetManager()
	fc, err := mgr.CreateContext(context.Background(), "spec", "spec ctx", []string{f}, ctxmgr.ModeFull, nil, false)
	if err != nil {
		t.Fatalf("CreateContext: %v", err)
	}
	if err := mgr.AttachContext("sess-agent", fc.ID, 0); err != nil {
		t.Fatalf("AttachContext: %v", err)
	}

	c.currentSessionName = "sess-agent"
	block := c.attachedContextAgentBlock()
	if !strings.Contains(block, "AGENT-CONTEXT-MARKER") {
		t.Errorf("attached context missing from agent block:\n%s", block)
	}

	c.currentSessionName = "another-session"
	if got := c.attachedContextAgentBlock(); strings.Contains(got, "AGENT-CONTEXT-MARKER") {
		t.Error("attached context leaked into a different session's agent block")
	}
}

// loopFakeManager embeds the interface so unimplemented methods exist; the
// model-cache refresh goroutine only needs ListModelsForProvider to not panic.
type loopFakeManager struct {
	manager.LLMManager
}

func (m *loopFakeManager) ListModelsForProvider(context.Context, string) ([]client.ModelInfo, error) {
	return nil, errors.New("fake manager: no live listing")
}

// TestRunLoopRPC_SessionScopesRun pins that RPCRunOpts.Session swaps
// cli.currentSessionName for the duration of the run (so /context and
// knowledge resolve like the REPL would) and restores it afterwards.
func TestRunLoopRPC_SessionScopesRun(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop(), manager: &loopFakeManager{}}
	c.currentSessionName = "repl"

	var seen string
	_, err := c.runLoopRPC(context.Background(), RPCRunOpts{Session: "mcp-sess"}, func(context.Context) error {
		seen = c.currentSessionName
		return nil
	})
	if err != nil {
		t.Fatalf("runLoopRPC: %v", err)
	}
	if seen != "mcp-sess" {
		t.Errorf("run should see the caller's session; got %q", seen)
	}
	if c.currentSessionName != "repl" {
		t.Errorf("currentSessionName not restored; got %q", c.currentSessionName)
	}

	// Empty session leaves the process default untouched.
	_, err = c.runLoopRPC(context.Background(), RPCRunOpts{}, func(context.Context) error {
		seen = c.currentSessionName
		return nil
	})
	if err != nil {
		t.Fatalf("runLoopRPC: %v", err)
	}
	if seen != "repl" {
		t.Errorf("empty session must keep the default; got %q", seen)
	}
}
