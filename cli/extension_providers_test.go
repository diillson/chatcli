/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/models"
)

// fakeToolCaller records server-scoped tool calls and answers from a table.
type fakeToolCaller struct {
	mu      sync.Mutex
	calls   []string // "server/tool"
	args    []map[string]interface{}
	answers map[string]string
	fail    map[string]bool
}

func (f *fakeToolCaller) ExecuteServerTool(_ context.Context, server, tool string, args map[string]interface{}) (*mcp.MCPToolResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := server + "/" + tool
	f.calls = append(f.calls, key)
	f.args = append(f.args, args)
	if f.fail[key] {
		return nil, errors.New("boom")
	}
	return &mcp.MCPToolResult{Content: f.answers[key]}, nil
}

func (f *fakeToolCaller) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == key {
			n++
		}
	}
	return n
}

func TestExtensionTarget(t *testing.T) {
	for raw, want := range map[string]string{"": "", "builtin": "", "MCP:memsvc": "memsvc", "mcp:": "", "http://x": "", " mcp:engine ": "engine"} {
		got, ok := extensionTarget(raw)
		if got != want || ok != (want != "") {
			t.Fatalf("%q → %q/%v", raw, got, ok)
		}
	}
	if extensionStatus("mcp:svc") != "mcp:svc" || extensionStatus("") != "builtin" {
		t.Fatal("status rendering")
	}
}

func TestExternalMemoryRecall_AugmentsAndDegrades(t *testing.T) {
	cli := newTenantTestCLI(t)
	f := &fakeToolCaller{answers: map[string]string{"memsvc/memory_recall": "- [external] the deploy freeze ends Friday"}, fail: map[string]bool{}}
	cli.extToolCaller = f

	t.Setenv(MemoryProviderEnv, "builtin")
	if got := cli.externalMemoryRecall(context.Background(), "when does the freeze end", []string{"freeze"}); got != "" {
		t.Fatal("builtin provider must not call out")
	}
	t.Setenv(MemoryProviderEnv, "mcp:memsvc")
	got := cli.externalMemoryRecall(context.Background(), "when does the freeze end", []string{"freeze"})
	if !strings.Contains(got, "deploy freeze") || f.count("memsvc/memory_recall") != 1 {
		t.Fatalf("recall = %q calls=%v", got, f.calls)
	}
	if f.args[0]["query"] != "when does the freeze end" || f.args[0]["budget_chars"] != extRecallBudget {
		t.Fatalf("args = %v", f.args[0])
	}
	// The auto-recall block carries the external answer even without a
	// memory store (no embedded facts).
	t.Setenv("CHATCLI_MEMORY_AUTORECALL", "true")
	block := cli.memoryAutoRecallBlockCtx(context.Background(), []string{"freeze"}, "when does the freeze end")
	if !strings.Contains(block, "deploy freeze") || !strings.HasPrefix(block, autoRecallHeader) {
		t.Fatalf("block = %q", block)
	}
	// Failure degrades to nothing.
	f.fail["memsvc/memory_recall"] = true
	if got := cli.externalMemoryRecall(context.Background(), "q", []string{"q"}); got != "" {
		t.Fatal("a failing provider must add nothing")
	}
}

func TestForwardNewHistory_TracksTheMark(t *testing.T) {
	cli := newTenantTestCLI(t)
	f := &fakeToolCaller{answers: map[string]string{}, fail: map[string]bool{}}
	cli.extToolCaller = f
	t.Setenv(MemoryProviderEnv, "mcp:memsvc")
	hist := []models.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "q1"}, {Role: "assistant", Content: "a1"}}
	cli.forwardNewHistory(context.Background(), cli.extForwardState(), hist, "work")
	waitCalls(t, f, "memsvc/memory_store", 1)
	if msgs, _ := f.args[0]["messages"].([]map[string]string); len(msgs) != 2 || msgs[0]["content"] != "q1" {
		t.Fatalf("system messages are skipped, turns forwarded: %v", f.args[0])
	}
	// Nothing new: no call.
	cli.forwardNewHistory(context.Background(), cli.extForwardState(), hist, "work")
	time.Sleep(50 * time.Millisecond)
	if f.count("memsvc/memory_store") != 1 {
		t.Fatal("no new messages must mean no call")
	}
	// New turn: only the delta.
	hist = append(hist, models.Message{Role: "user", Content: "q2"})
	cli.forwardNewHistory(context.Background(), cli.extForwardState(), hist, "work")
	waitCalls(t, f, "memsvc/memory_store", 2)
	if msgs, _ := f.args[1]["messages"].([]map[string]string); len(msgs) != 1 || msgs[0]["content"] != "q2" {
		t.Fatalf("delta = %v", f.args[1])
	}
	// Shrunk history (compaction/clear) resets the mark instead of skipping.
	cli.forwardNewHistory(context.Background(), cli.extForwardState(), hist[:1], "work")
	cli.forwardNewHistory(context.Background(), cli.extForwardState(), append(hist[:1], models.Message{Role: "user", Content: "after clear"}), "work")
	waitCalls(t, f, "memsvc/memory_store", 3)
}

func waitCalls(t *testing.T, f *fakeToolCaller, key string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for f.count(key) < n {
		if time.Now().After(deadline) {
			t.Fatalf("expected %d calls to %s, got %d", n, key, f.count(key))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExternalSummarizer_ConfiguredAndFallbackSignals(t *testing.T) {
	cli := newTenantTestCLI(t)
	f := &fakeToolCaller{answers: map[string]string{"engine/context_compact": "SUMMARY"}, fail: map[string]bool{}}
	cli.extToolCaller = f
	t.Setenv(ContextEngineEnv, "")
	if cli.externalSummarizer() != nil {
		t.Fatal("builtin engine must yield no external summarizer")
	}
	t.Setenv(ContextEngineEnv, "mcp:engine")
	ext := cli.externalSummarizer()
	if ext == nil {
		t.Fatal("configured engine must yield a summarizer")
	}
	out, err := ext.Compact(context.Background(), "[user]: hello", 5000, "keep names")
	if err != nil || out != "SUMMARY" || f.args[0]["instruction"] != "keep names" || f.args[0]["budget_chars"] != 5000 {
		t.Fatalf("out=%q err=%v args=%v", out, err, f.args[0])
	}
	f.answers["engine/context_compact"] = ""
	if _, err := ext.Compact(context.Background(), "x", 1, ""); err == nil {
		t.Fatal("an empty summary must be an error so the compactor falls back")
	}
	f.fail["engine/context_compact"] = true
	if _, err := ext.Compact(context.Background(), "x", 1, ""); err == nil {
		t.Fatal("a failing engine must surface the error")
	}
}
