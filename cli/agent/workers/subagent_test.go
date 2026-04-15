package workers

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// simpleStubClient returns a single pre-canned response and ignores the prompt.
type simpleStubClient struct{ reply string }

func (c *simpleStubClient) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	return c.reply, nil
}
func (c *simpleStubClient) GetModelName() string { return "stub" }

func TestParseDelegateArgs_Native(t *testing.T) {
	native := map[string]interface{}{
		"prompt":      "summarize the metrics",
		"description": "metrics summary",
		"tools":       []interface{}{"read", "search"},
		"read_only":   true,
		"max_turns":   float64(5),
	}
	args, err := parseDelegateArgs(native, "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if args.Prompt != "summarize the metrics" {
		t.Fatalf("prompt mismatch: %q", args.Prompt)
	}
	if args.Description != "metrics summary" {
		t.Fatalf("description mismatch: %q", args.Description)
	}
	if len(args.Tools) != 2 || args.Tools[0] != "read" {
		t.Fatalf("tools mismatch: %v", args.Tools)
	}
	if args.ReadOnly == nil || !*args.ReadOnly {
		t.Fatalf("read_only should be true")
	}
	if args.MaxTurns != 5 {
		t.Fatalf("max_turns = %d, want 5", args.MaxTurns)
	}
}

func TestParseDelegateArgs_ToolsString(t *testing.T) {
	// Some models pass tools as a comma-separated string instead of an array.
	native := map[string]interface{}{
		"prompt": "do X",
		"tools":  "read, tree,search",
	}
	args, err := parseDelegateArgs(native, "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"read", "tree", "search"}
	if len(args.Tools) != len(want) {
		t.Fatalf("tools = %v, want %v", args.Tools, want)
	}
	for i, tool := range args.Tools {
		if tool != want[i] {
			t.Fatalf("tool[%d] = %q, want %q", i, tool, want[i])
		}
	}
}

func TestParseDelegateArgs_RawJSON(t *testing.T) {
	raw := `{"prompt":"raw form","description":"rawdesc"}`
	args, err := parseDelegateArgs(nil, raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if args.Prompt != "raw form" || args.Description != "rawdesc" {
		t.Fatalf("bad parse: %+v", args)
	}
}

func TestParseDelegateArgs_MissingPromptErrors(t *testing.T) {
	_, err := parseDelegateArgs(map[string]interface{}{"description": "x"}, "")
	if err == nil {
		t.Fatal("expected error when prompt missing")
	}
}

func TestRunSubagent_RequiresContext(t *testing.T) {
	// With no subagentContext in ctx, runSubagent must refuse.
	_, err := runSubagent(context.Background(), delegateArgs{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "LLM client") {
		t.Fatalf("expected LLM-client error, got %v", err)
	}
}

func TestRunSubagent_DepthLimit(t *testing.T) {
	// Already at depth equal to max — refuse further recursion.
	t.Setenv("CHATCLI_AGENT_SUBAGENT_MAX_DEPTH", "1")
	ctx := withSubagentContext(context.Background(), subagentContext{
		Depth:     1,
		LLMClient: &simpleStubClient{reply: "ignored"},
		Logger:    zap.NewNop(),
	})
	_, err := runSubagent(ctx, delegateArgs{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "subagent depth") {
		t.Fatalf("expected depth limit error, got %v", err)
	}
}

func TestRunSubagent_ShortCircuitNoTools(t *testing.T) {
	// Stub LLM client that returns final text immediately — exercises the
	// full subagent plumbing without needing tool execution.
	stub := &simpleStubClient{reply: "42"}
	ctx := withSubagentContext(context.Background(), subagentContext{
		Depth:     0,
		LLMClient: stub,
		Skills:    NewSkillSet(),
		Logger:    zap.NewNop(),
	})
	out, err := runSubagent(ctx, delegateArgs{Prompt: "what is the answer", Description: "oracle"})
	if err != nil {
		t.Fatalf("subagent error: %v", err)
	}
	if !strings.Contains(out, "42") {
		t.Fatalf("expected subagent output to contain the stub reply, got %q", out)
	}
	if !strings.Contains(out, "oracle") {
		t.Fatalf("expected result header to mention description, got %q", out)
	}
}
