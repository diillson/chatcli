/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/cli/agentevents"
)

// collectLines is a thread-safe emit collector.
type collectLines struct {
	mu    sync.Mutex
	lines []string
}

func (c *collectLines) emit(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, s)
}

func (c *collectLines) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.lines))
	copy(out, c.lines)
	return out
}

func (c *collectLines) joined() string { return strings.Join(c.snapshot(), "\n") }

func TestGatewayEventsSinkLines(t *testing.T) {
	col := &collectLines{}
	sink := newGatewayEventsSink(col.emit)

	sink.Thought("Reading the config\nto understand defaults")
	sink.ToolStart(agentevents.ToolCall{Name: "@coder", Title: "Reading: main.go"})
	sink.ToolEnd(agentevents.ToolCall{Title: "Reading: main.go", Duration: 120 * time.Millisecond})
	sink.ToolEnd(agentevents.ToolCall{Title: "exec go test", IsError: true})
	sink.Message("final answer must NOT appear in progress")
	sink.PlanUpdate(agentevents.Plan{Entries: []agentevents.PlanEntry{
		{Content: "read files", Status: "completed"},
		{Content: "apply patch", Status: "in_progress"},
		{Content: "run tests", Status: "pending"},
	}})

	out := col.joined()
	for _, want := range []string{
		"🧠 Reading the config to understand defaults",
		"▸ Reading: main.go",
		"✓ Reading: main.go (120ms)",
		"✗ exec go test",
		"📋 1/3 · apply patch",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "final answer") {
		t.Fatalf("Message leaked into progress:\n%s", out)
	}
}

func TestGatewayEventsSinkDedupsConsecutive(t *testing.T) {
	col := &collectLines{}
	sink := newGatewayEventsSink(col.emit)
	sink.Thought("same")
	sink.Thought("same")
	sink.Thought("different")
	if got := len(col.snapshot()); got != 2 {
		t.Fatalf("expected 2 lines after dedup, got %d: %v", got, col.snapshot())
	}
}

func TestWatchRunsProgressEmitsChangesAndTerminal(t *testing.T) {
	reg := runs.NewRegistry(10)
	col := &collectLines{}
	stop := watchRunsProgress(context.Background(), reg, col.emit, 10*time.Millisecond)

	_, worker := reg.Begin(context.Background(), runs.Info{
		Kind: runs.KindWorker, Agent: "reviewer", Task: "review the diff",
	})
	worker.SetTurn(2, 30)
	worker.SetAction("read cli/foo.go")

	if !waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(col.joined(), "🤖 [reviewer]") &&
			strings.Contains(col.joined(), "read cli/foo.go")
	}) {
		t.Fatalf("live line not emitted:\n%s", col.joined())
	}

	worker.End(nil)
	if !waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(col.joined(), "✓ [reviewer]")
	}) {
		t.Fatalf("terminal line not emitted:\n%s", col.joined())
	}

	stop()
	// After stop, no further emissions.
	n := len(col.snapshot())
	_, w2 := reg.Begin(context.Background(), runs.Info{Kind: runs.KindWorker, Agent: "coder"})
	time.Sleep(50 * time.Millisecond)
	w2.End(nil)
	if len(col.snapshot()) != n {
		t.Fatal("watcher emitted after stop")
	}
}

func TestWatchRunsProgressIgnoresOrchestrator(t *testing.T) {
	reg := runs.NewRegistry(10)
	col := &collectLines{}
	stop := watchRunsProgress(context.Background(), reg, col.emit, 10*time.Millisecond)
	defer stop()

	_, orch := reg.Begin(context.Background(), runs.Info{Kind: runs.KindOrchestrator, Agent: "coder"})
	defer orch.End(nil)
	time.Sleep(60 * time.Millisecond)
	if len(col.snapshot()) != 0 {
		t.Fatalf("orchestrator must not be narrated: %v", col.snapshot())
	}
}
