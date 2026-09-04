/*
 * ChatCLI - Auto-recall wiring tests (agent workspace blocks).
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// newAgentAutoRecallCLI wires the minimal agent stack: memory store with a
// seeded fact, a context builder over it, and recent history whose keywords
// hit the fact.
func newAgentAutoRecallCLI(t *testing.T) (*ChatCLI, *AgentMode) {
	t.Helper()
	dir := t.TempDir()
	ms := workspace.NewMemoryStore(dir, zap.NewNop())
	if !ms.Manager().Facts.AddFact("embed.FS requires forward slashes, never filepath.Join", "gotcha", []string{"embed", "windows"}) {
		t.Fatal("seed fact")
	}
	cli := &ChatCLI{
		memoryStore:    ms,
		contextBuilder: workspace.NewContextBuilder(workspace.NewBootstrapLoader(dir, dir, zap.NewNop()), ms, dir),
		logger:         zap.NewNop(),
	}
	cli.history = []models.Message{
		{Role: "user", Content: "why does embed break on windows paths?"},
	}
	return cli, NewAgentMode(cli, zap.NewNop())
}

func TestBuildWorkspaceBlocks_InjectsAutoRecallInIndexMode(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_MODE", "index")
	cli, a := newAgentAutoRecallCLI(t)

	workspaceText, dynamicText := a.buildWorkspaceBlocks(context.Background(), "embed windows")
	if !strings.Contains(dynamicText, "[MEMORY AUTO-RECALL]") || !strings.Contains(dynamicText, "forward slashes") {
		t.Errorf("index mode must inject auto-recall into the UNCACHED dynamic block, got: %s", dynamicText)
	}
	if strings.Contains(workspaceText, "[MEMORY AUTO-RECALL]") {
		t.Errorf("auto-recall must NEVER land in the cacheable workspace block: %s", workspaceText)
	}
	if !strings.Contains(dynamicText, "Current date:") {
		t.Errorf("the date context must survive alongside auto-recall: %s", dynamicText)
	}
	_ = cli
}

func TestBuildWorkspaceBlocks_NoAutoRecallInFullMode(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_MODE", "full")
	_, a := newAgentAutoRecallCLI(t)

	_, dynamicText := a.buildWorkspaceBlocks(context.Background(), "embed windows")
	if strings.Contains(dynamicText, "[MEMORY AUTO-RECALL]") {
		t.Errorf("full mode already pushes retrieval; auto-recall must not double-inject: %s", dynamicText)
	}
}

func TestShowConfigMemory_ExposesAutoRecall(t *testing.T) {
	c := minimalCLI(t)
	out := captureStdout(t, func() { c.showConfigMemory() })
	if !strings.Contains(out, "CHATCLI_MEMORY_AUTORECALL") {
		t.Errorf("config memory must expose the auto-recall gate, got: %s", out)
	}
	if !strings.Contains(out, "CHATCLI_MEMORY_MODE") {
		t.Errorf("config memory must keep the mode line, got: %s", out)
	}
}
