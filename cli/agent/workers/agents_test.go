package workers

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// --- Test all 6 agents' interface methods ---

func TestFileAgent_Interface(t *testing.T) {
	a := NewFileAgent()
	if a.Type() != AgentTypeFile {
		t.Errorf("expected type %s, got %s", AgentTypeFile, a.Type())
	}
	if a.Name() != "FileAgent" {
		t.Errorf("expected name 'FileAgent', got '%s'", a.Name())
	}
	if !a.IsReadOnly() {
		t.Error("FileAgent should be read-only")
	}
	cmds := a.AllowedCommands()
	if len(cmds) != 3 {
		t.Errorf("expected 3 allowed commands, got %d", len(cmds))
	}
	if a.Description() == "" {
		t.Error("expected non-empty description")
	}
	if a.SystemPrompt() == "" {
		t.Error("expected non-empty system prompt")
	}
	if a.Skills() == nil {
		t.Error("expected non-nil skills")
	}
	// Verify skills were registered
	skills := a.Skills().List()
	if len(skills) < 4 {
		t.Errorf("expected at least 4 skills, got %d", len(skills))
	}
	// batch-read should be executable
	sk, ok := a.Skills().Get("batch-read")
	if !ok {
		t.Fatal("expected batch-read skill")
	}
	if sk.Type != SkillExecutable {
		t.Error("expected batch-read to be executable")
	}
}

func TestCoderAgent_Interface(t *testing.T) {
	a := NewCoderAgent()
	if a.Type() != AgentTypeCoder {
		t.Errorf("expected type %s, got %s", AgentTypeCoder, a.Type())
	}
	if a.Name() != "CoderAgent" {
		t.Errorf("expected name 'CoderAgent', got '%s'", a.Name())
	}
	if a.IsReadOnly() {
		t.Error("CoderAgent should NOT be read-only")
	}
	cmds := a.AllowedCommands()
	if len(cmds) != 4 {
		t.Errorf("expected 4 allowed commands, got %d", len(cmds))
	}
	if a.Description() == "" {
		t.Error("expected non-empty description")
	}
	if a.SystemPrompt() == "" {
		t.Error("expected non-empty system prompt")
	}
	if a.Skills() == nil {
		t.Error("expected non-nil skills")
	}
	skills := a.Skills().List()
	if len(skills) < 4 {
		t.Errorf("expected at least 4 skills, got %d", len(skills))
	}
}

func TestShellAgent_Interface(t *testing.T) {
	a := NewShellAgent()
	if a.Type() != AgentTypeShell {
		t.Errorf("expected type %s, got %s", AgentTypeShell, a.Type())
	}
	if a.Name() != "ShellAgent" {
		t.Errorf("expected name 'ShellAgent', got '%s'", a.Name())
	}
	if a.IsReadOnly() {
		t.Error("ShellAgent should NOT be read-only")
	}
	cmds := a.AllowedCommands()
	if len(cmds) != 2 {
		t.Errorf("expected 2 allowed commands, got %d", len(cmds))
	}
	if a.Description() == "" {
		t.Error("expected non-empty description")
	}
	if a.SystemPrompt() == "" {
		t.Error("expected non-empty system prompt")
	}
	skills := a.Skills().List()
	if len(skills) < 3 {
		t.Errorf("expected at least 3 skills, got %d", len(skills))
	}
	// run-tests should be executable
	sk, ok := a.Skills().Get("run-tests")
	if !ok {
		t.Fatal("expected run-tests skill")
	}
	if sk.Type != SkillExecutable {
		t.Error("expected run-tests to be executable")
	}
}

func TestGitAgent_Interface(t *testing.T) {
	a := NewGitAgent()
	if a.Type() != AgentTypeGit {
		t.Errorf("expected type %s, got %s", AgentTypeGit, a.Type())
	}
	if a.Name() != "GitAgent" {
		t.Errorf("expected name 'GitAgent', got '%s'", a.Name())
	}
	if a.IsReadOnly() {
		t.Error("GitAgent should NOT be read-only")
	}
	cmds := a.AllowedCommands()
	if len(cmds) != 6 {
		t.Errorf("expected 6 allowed commands, got %d", len(cmds))
	}
	if a.Description() == "" {
		t.Error("expected non-empty description")
	}
	if a.SystemPrompt() == "" {
		t.Error("expected non-empty system prompt")
	}
	skills := a.Skills().List()
	if len(skills) < 3 {
		t.Errorf("expected at least 3 skills, got %d", len(skills))
	}
	sk, ok := a.Skills().Get("smart-commit")
	if !ok {
		t.Fatal("expected smart-commit skill")
	}
	if sk.Type != SkillExecutable {
		t.Error("expected smart-commit to be executable")
	}
}

func TestSearchAgent_Interface(t *testing.T) {
	a := NewSearchAgent()
	if a.Type() != AgentTypeSearch {
		t.Errorf("expected type %s, got %s", AgentTypeSearch, a.Type())
	}
	if a.Name() != "SearchAgent" {
		t.Errorf("expected name 'SearchAgent', got '%s'", a.Name())
	}
	if !a.IsReadOnly() {
		t.Error("SearchAgent should be read-only")
	}
	cmds := a.AllowedCommands()
	if len(cmds) != 3 {
		t.Errorf("expected 3 allowed commands, got %d", len(cmds))
	}
	if a.Description() == "" {
		t.Error("expected non-empty description")
	}
	if a.SystemPrompt() == "" {
		t.Error("expected non-empty system prompt")
	}
	skills := a.Skills().List()
	if len(skills) < 4 {
		t.Errorf("expected at least 4 skills, got %d", len(skills))
	}
	sk, ok := a.Skills().Get("map-project")
	if !ok {
		t.Fatal("expected map-project skill")
	}
	if sk.Type != SkillExecutable {
		t.Error("expected map-project to be executable")
	}
}

func TestPlannerAgent_Interface(t *testing.T) {
	a := NewPlannerAgent()
	if a.Type() != AgentTypePlanner {
		t.Errorf("expected type %s, got %s", AgentTypePlanner, a.Type())
	}
	if a.Name() != "PlannerAgent" {
		t.Errorf("expected name 'PlannerAgent', got '%s'", a.Name())
	}
	if !a.IsReadOnly() {
		t.Error("PlannerAgent should be read-only")
	}
	cmds := a.AllowedCommands()
	if len(cmds) != 0 {
		t.Errorf("expected 0 allowed commands, got %d", len(cmds))
	}
	if a.Description() == "" {
		t.Error("expected non-empty description")
	}
	if a.SystemPrompt() == "" {
		t.Error("expected non-empty system prompt")
	}
	skills := a.Skills().List()
	if len(skills) < 3 {
		t.Errorf("expected at least 3 skills, got %d", len(skills))
	}
}

// --- Test Execute for agents that use RunWorkerReAct ---

func TestFileAgent_Execute(t *testing.T) {
	a := NewFileAgent()
	client := &mockLLMClient{responses: []string{"File contents summary."}}
	deps := &WorkerDeps{
		LLMClient: client,
		LockMgr:   NewFileLockManager(),
		Logger:    zap.NewNop(),
	}

	result, err := a.Execute(context.Background(), "read all go files", deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Agent != AgentTypeFile {
		t.Errorf("expected agent type %s, got %s", AgentTypeFile, result.Agent)
	}
	if result.Task != "read all go files" {
		t.Errorf("expected task preserved, got '%s'", result.Task)
	}
}

func TestCoderAgent_Execute(t *testing.T) {
	a := NewCoderAgent()
	client := &mockLLMClient{responses: []string{"Code written successfully."}}
	deps := &WorkerDeps{
		LLMClient: client,
		LockMgr:   NewFileLockManager(),
		Logger:    zap.NewNop(),
	}

	result, err := a.Execute(context.Background(), "write handler.go", deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Agent != AgentTypeCoder {
		t.Errorf("expected agent type %s, got %s", AgentTypeCoder, result.Agent)
	}
}

func TestShellAgent_Execute(t *testing.T) {
	a := NewShellAgent()
	client := &mockLLMClient{responses: []string{"Tests passed."}}
	deps := &WorkerDeps{
		LLMClient: client,
		LockMgr:   NewFileLockManager(),
		Logger:    zap.NewNop(),
	}

	result, err := a.Execute(context.Background(), "run tests", deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Agent != AgentTypeShell {
		t.Errorf("expected agent type %s, got %s", AgentTypeShell, result.Agent)
	}
}

func TestGitAgent_Execute(t *testing.T) {
	a := NewGitAgent()
	client := &mockLLMClient{responses: []string{"Repository is clean."}}
	deps := &WorkerDeps{
		LLMClient: client,
		LockMgr:   NewFileLockManager(),
		Logger:    zap.NewNop(),
	}

	result, err := a.Execute(context.Background(), "check git status", deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Agent != AgentTypeGit {
		t.Errorf("expected agent type %s, got %s", AgentTypeGit, result.Agent)
	}
}

func TestSearchAgent_Execute(t *testing.T) {
	a := NewSearchAgent()
	client := &mockLLMClient{responses: []string{"Found 3 usages."}}
	deps := &WorkerDeps{
		LLMClient: client,
		LockMgr:   NewFileLockManager(),
		Logger:    zap.NewNop(),
	}

	result, err := a.Execute(context.Background(), "find usages of Engine", deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Agent != AgentTypeSearch {
		t.Errorf("expected agent type %s, got %s", AgentTypeSearch, result.Agent)
	}
}

func TestPlannerAgent_Execute(t *testing.T) {
	a := NewPlannerAgent()
	client := &mockLLMClient{responses: []string{"## Plan\n1. Read files\n2. Modify code\n3. Test"}}
	deps := &WorkerDeps{
		LLMClient: client,
		Logger:    zap.NewNop(),
	}

	result, err := a.Execute(context.Background(), "plan refactoring", deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Agent != AgentTypePlanner {
		t.Errorf("expected agent type %s, got %s", AgentTypePlanner, result.Agent)
	}
	if result.Output == "" {
		t.Error("expected non-empty output from planner")
	}
}

func TestPlannerAgent_ExecuteError(t *testing.T) {
	a := NewPlannerAgent()
	client := &errorLLMClient{err: context.DeadlineExceeded}
	deps := &WorkerDeps{
		LLMClient: client,
		Logger:    zap.NewNop(),
	}

	result, err := a.Execute(context.Background(), "plan something", deps)
	if err == nil {
		t.Fatal("expected error from planner with failing LLM")
	}
	if result == nil {
		t.Fatal("expected non-nil result even on error")
	}
	if result.Error == nil {
		t.Error("expected error in result")
	}
}

// --- Test that all agents conform to WorkerAgent interface ---

func TestAllAgents_ImplementWorkerAgent(t *testing.T) {
	agents := []WorkerAgent{
		NewFileAgent(),
		NewCoderAgent(),
		NewShellAgent(),
		NewGitAgent(),
		NewSearchAgent(),
		NewPlannerAgent(),
	}

	for _, a := range agents {
		if a.Type() == "" {
			t.Errorf("agent %s has empty type", a.Name())
		}
		if a.Name() == "" {
			t.Errorf("agent %s has empty name", a.Type())
		}
		if a.Description() == "" {
			t.Errorf("agent %s has empty description", a.Type())
		}
		if a.SystemPrompt() == "" {
			t.Errorf("agent %s has empty system prompt", a.Type())
		}
		if a.Skills() == nil {
			t.Errorf("agent %s has nil skills", a.Type())
		}
	}
}

// errorLLMClient is already defined in worker_react_test.go but we need
// to use models import to avoid unused import warning.
var _ models.Message
