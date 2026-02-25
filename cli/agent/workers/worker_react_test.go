package workers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestRunWorkerReAct_SingleTurnNoTools(t *testing.T) {
	client := &mockLLMClient{responses: []string{"Here is the final answer."}}
	logger := zap.NewNop()
	config := WorkerReActConfig{
		MaxTurns:        5,
		SystemPrompt:    "You are a test agent.",
		AllowedCommands: []string{"read"},
		ReadOnly:        true,
	}

	result, err := RunWorkerReAct(context.Background(), config, "do something", client, nil, NewSkillSet(), logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !strings.Contains(result.Output, "final answer") {
		t.Errorf("expected output to contain final answer, got: %s", result.Output)
	}
	if result.CallID == "" {
		t.Error("expected non-empty CallID")
	}
	if result.Duration <= 0 {
		t.Error("expected positive duration")
	}
}

func TestRunWorkerReAct_DefaultMaxTurns(t *testing.T) {
	client := &mockLLMClient{responses: []string{"done"}}
	logger := zap.NewNop()
	config := WorkerReActConfig{
		MaxTurns:     0, // should default to DefaultWorkerMaxTurns
		SystemPrompt: "test",
	}

	result, err := RunWorkerReAct(context.Background(), config, "task", client, nil, NewSkillSet(), logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestRunWorkerReAct_LLMError(t *testing.T) {
	client := &errorLLMClient{err: fmt.Errorf("connection refused")}
	logger := zap.NewNop()
	config := WorkerReActConfig{
		MaxTurns:     3,
		SystemPrompt: "test",
	}

	result, err := RunWorkerReAct(context.Background(), config, "task", client, nil, NewSkillSet(), logger)
	if err == nil {
		t.Fatal("expected error")
	}
	if result == nil {
		t.Fatal("expected non-nil result even on error")
	}
	if result.Error == nil {
		t.Error("expected error in result")
	}
}

func TestRunWorkerReAct_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	client := &mockLLMClient{responses: []string{"should not reach"}}
	logger := zap.NewNop()
	config := WorkerReActConfig{
		MaxTurns:     5,
		SystemPrompt: "test",
	}

	result, err := RunWorkerReAct(ctx, config, "task", client, nil, NewSkillSet(), logger)
	if err == nil {
		t.Fatal("expected context error")
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestRunWorkerReAct_BlockedCommand(t *testing.T) {
	response := `<tool_call name="@coder" args="exec --cmd ls" />`
	client := &mockLLMClient{responses: []string{response, "Done, command was blocked."}}
	logger := zap.NewNop()
	config := WorkerReActConfig{
		MaxTurns:        5,
		SystemPrompt:    "test",
		AllowedCommands: []string{"read"},
		ReadOnly:        false,
	}

	result, err := RunWorkerReAct(context.Background(), config, "run ls", client, nil, NewSkillSet(), logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Output, "BLOCKED") {
		t.Errorf("expected BLOCKED in output, got: %s", result.Output)
	}
}

func TestRunWorkerReAct_ReadOnlyBlocksWrite(t *testing.T) {
	response := `<tool_call name="@coder" args="write --file test.go --content hello" />`
	client := &mockLLMClient{responses: []string{response, "Blocked."}}
	logger := zap.NewNop()
	config := WorkerReActConfig{
		MaxTurns:        5,
		SystemPrompt:    "test",
		AllowedCommands: []string{"write", "read"},
		ReadOnly:        true,
	}

	result, err := RunWorkerReAct(context.Background(), config, "write file", client, nil, NewSkillSet(), logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Output, "read-only") {
		t.Errorf("expected read-only block in output, got: %s", result.Output)
	}
}

func TestRunWorkerReAct_WithToolExecution(t *testing.T) {
	response1 := `<tool_call name="@coder" args="read --file worker_react_test.go" />`
	response2 := "I read the file. Done."
	client := &mockLLMClient{responses: []string{response1, response2}}
	logger := zap.NewNop()
	lockMgr := NewFileLockManager()
	config := WorkerReActConfig{
		MaxTurns:        5,
		SystemPrompt:    "test",
		AllowedCommands: []string{"read"},
		ReadOnly:        true,
	}

	result, err := RunWorkerReAct(context.Background(), config, "read a file", client, lockMgr, NewSkillSet(), logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result")
	}
	if len(result.ToolCalls) == 0 {
		t.Error("expected at least one tool call recorded")
	}
}

func TestRunWorkerReAct_WriteLockAcquired(t *testing.T) {
	response1 := `<tool_call name="@coder" args='{"cmd":"write","args":{"file":"/tmp/test_lock.go","content":"pkg"}}' />`
	response2 := "Done writing."
	client := &mockLLMClient{responses: []string{response1, response2}}
	logger := zap.NewNop()
	lockMgr := NewFileLockManager()
	config := WorkerReActConfig{
		MaxTurns:        5,
		SystemPrompt:    "test",
		AllowedCommands: []string{"write", "read"},
		ReadOnly:        false,
	}

	result, err := RunWorkerReAct(context.Background(), config, "write file", client, lockMgr, NewSkillSet(), logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result")
	}
}

func TestRunWorkerReAct_MaxTurnsReached(t *testing.T) {
	toolResponse := `<tool_call name="@coder" args="read --file main.go" />`
	responses := make([]string, 20)
	for i := range responses {
		responses[i] = toolResponse
	}
	client := &mockLLMClient{responses: responses}
	logger := zap.NewNop()
	config := WorkerReActConfig{
		MaxTurns:        3,
		SystemPrompt:    "test",
		AllowedCommands: []string{"read"},
		ReadOnly:        true,
	}

	result, err := RunWorkerReAct(context.Background(), config, "infinite task", client, nil, NewSkillSet(), logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result")
	}
	if len(result.ToolCalls) != 3 {
		t.Errorf("expected 3 tool calls for 3 turns, got %d", len(result.ToolCalls))
	}
}

// --- parseCoderToolCall tests ---

func TestParseCoderToolCall_JSON(t *testing.T) {
	tc := agent.ToolCall{Name: "@coder", Args: `{"cmd":"read","args":{"file":"main.go"}}`}
	cmd, args, err := parseCoderToolCall(tc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "read" {
		t.Errorf("expected cmd 'read', got '%s'", cmd)
	}
	found := false
	for _, a := range args {
		if a == "main.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'main.go' in args, got %v", args)
	}
}

func TestParseCoderToolCall_JSONCmdOnly(t *testing.T) {
	tc := agent.ToolCall{Name: "@coder", Args: `{"cmd":"tree","args":"invalid"}`}
	cmd, args, err := parseCoderToolCall(tc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "tree" {
		t.Errorf("expected cmd 'tree', got '%s'", cmd)
	}
	if args != nil {
		t.Errorf("expected nil args for non-map args, got %v", args)
	}
}

func TestParseCoderToolCall_CLI(t *testing.T) {
	tc := agent.ToolCall{Name: "@coder", Args: "read --file main.go"}
	cmd, args, err := parseCoderToolCall(tc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "read" {
		t.Errorf("expected cmd 'read', got '%s'", cmd)
	}
	if len(args) != 2 || args[0] != "--file" || args[1] != "main.go" {
		t.Errorf("expected ['--file', 'main.go'], got %v", args)
	}
}

func TestParseCoderToolCall_Empty(t *testing.T) {
	tc := agent.ToolCall{Name: "@coder", Args: ""}
	_, _, err := parseCoderToolCall(tc)
	if err == nil {
		t.Error("expected error for empty args")
	}
}

func TestParseCoderToolCall_JSONMultipleArgs(t *testing.T) {
	tc := agent.ToolCall{Name: "@coder", Args: `{"cmd":"search","args":{"term":"func","dir":".","glob":"*.go"}}`}
	cmd, args, err := parseCoderToolCall(tc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "search" {
		t.Errorf("expected cmd 'search', got '%s'", cmd)
	}
	if len(args) < 4 {
		t.Errorf("expected at least 4 args, got %d: %v", len(args), args)
	}
}

// --- isWriteCommand tests ---

func TestIsWriteCommand(t *testing.T) {
	writeCommands := []string{"write", "patch", "exec", "test", "rollback", "clean"}
	for _, cmd := range writeCommands {
		if !isWriteCommand(cmd) {
			t.Errorf("expected %q to be a write command", cmd)
		}
	}

	readCommands := []string{"read", "tree", "search", "git-status", "git-diff"}
	for _, cmd := range readCommands {
		if isWriteCommand(cmd) {
			t.Errorf("expected %q to NOT be a write command", cmd)
		}
	}
}

// --- extractFilePathFromArgs tests ---

func TestExtractFilePathFromArgs_JSON(t *testing.T) {
	args := `{"cmd":"write","args":{"file":"/tmp/test.go","content":"hello"}}`
	path := extractFilePathFromArgs(args)
	if path != "/tmp/test.go" {
		t.Errorf("expected '/tmp/test.go', got '%s'", path)
	}
}

func TestExtractFilePathFromArgs_CLI(t *testing.T) {
	args := "write --file /tmp/test.go --content hello"
	path := extractFilePathFromArgs(args)
	if path != "/tmp/test.go" {
		t.Errorf("expected '/tmp/test.go', got '%s'", path)
	}
}

func TestExtractFilePathFromArgs_CLIShortFlag(t *testing.T) {
	args := "write -f /tmp/test.go"
	path := extractFilePathFromArgs(args)
	if path != "/tmp/test.go" {
		t.Errorf("expected '/tmp/test.go', got '%s'", path)
	}
}

func TestExtractFilePathFromArgs_NoFile(t *testing.T) {
	args := "tree --dir ."
	path := extractFilePathFromArgs(args)
	if path != "" {
		t.Errorf("expected empty path, got '%s'", path)
	}
}

func TestExtractFilePathFromArgs_FlagAtEnd(t *testing.T) {
	args := "write --file"
	path := extractFilePathFromArgs(args)
	if path != "" {
		t.Errorf("expected empty path for flag at end, got '%s'", path)
	}
}

// --- helpers ---

// errorLLMClient always returns an error.
type errorLLMClient struct {
	err error
}

func (c *errorLLMClient) GetModelName() string { return "error-mock" }
func (c *errorLLMClient) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	return "", c.err
}

// Ensure imports are used.
var _ = time.Millisecond
