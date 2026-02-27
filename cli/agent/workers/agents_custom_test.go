package workers

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/persona"
	"go.uber.org/zap"
)

// --- MapToolsToCommands tests ---

func TestMapToolsToCommands_FullSet(t *testing.T) {
	tools := []string{"Read", "Grep", "Glob", "Bash", "Write", "Edit", "Agent"}
	cmds := MapToolsToCommands(tools)

	expected := map[string]bool{
		"read": true, "search": true, "tree": true,
		"exec": true, "test": true, "write": true, "patch": true,
		"git-status": true, "git-diff": true, "git-log": true,
		"git-changed": true, "git-branch": true,
	}

	if len(cmds) != len(expected) {
		t.Errorf("expected %d commands, got %d: %v", len(expected), len(cmds), cmds)
	}
	for _, cmd := range cmds {
		if !expected[cmd] {
			t.Errorf("unexpected command: %s", cmd)
		}
	}
}

func TestMapToolsToCommands_ReadOnly(t *testing.T) {
	tools := []string{"Read", "Grep", "Glob"}
	cmds := MapToolsToCommands(tools)

	cmdSet := make(map[string]bool)
	for _, c := range cmds {
		cmdSet[c] = true
	}

	if len(cmds) != 3 {
		t.Errorf("expected 3 commands, got %d: %v", len(cmds), cmds)
	}
	if !cmdSet["read"] || !cmdSet["search"] || !cmdSet["tree"] {
		t.Errorf("expected read, search, tree; got %v", cmds)
	}
}

func TestMapToolsToCommands_Empty(t *testing.T) {
	cmds := MapToolsToCommands(nil)
	if len(cmds) != 0 {
		t.Errorf("expected 0 commands for nil tools, got %d", len(cmds))
	}

	cmds = MapToolsToCommands([]string{})
	if len(cmds) != 0 {
		t.Errorf("expected 0 commands for empty tools, got %d", len(cmds))
	}
}

func TestMapToolsToCommands_UnknownToolsIgnored(t *testing.T) {
	tools := []string{"Read", "ViewCodeItem", "FindByName"}
	cmds := MapToolsToCommands(tools)
	if len(cmds) != 1 || cmds[0] != "read" {
		t.Errorf("expected only 'read', got %v", cmds)
	}
}

func TestMapToolsToCommands_BashAddsGitCommands(t *testing.T) {
	tools := []string{"Bash"}
	cmds := MapToolsToCommands(tools)
	cmdSet := make(map[string]bool)
	for _, c := range cmds {
		cmdSet[c] = true
	}
	for _, gc := range []string{"exec", "test", "git-status", "git-diff", "git-log", "git-changed", "git-branch"} {
		if !cmdSet[gc] {
			t.Errorf("expected %q in commands, got %v", gc, cmds)
		}
	}
}

func TestMapToolsToCommands_NoDuplicates(t *testing.T) {
	tools := []string{"Read", "Read", "Bash", "Bash"}
	cmds := MapToolsToCommands(tools)
	seen := make(map[string]bool)
	for _, c := range cmds {
		if seen[c] {
			t.Errorf("duplicate command: %s", c)
		}
		seen[c] = true
	}
}

// --- isReadOnlyToolSet tests ---

func TestIsReadOnlyToolSet_ReadOnly(t *testing.T) {
	if !isReadOnlyToolSet([]string{"Read", "Grep", "Glob"}) {
		t.Error("expected read-only for Read, Grep, Glob")
	}
}

func TestIsReadOnlyToolSet_EmptyIsReadOnly(t *testing.T) {
	if !isReadOnlyToolSet(nil) {
		t.Error("expected read-only for nil tools")
	}
	if !isReadOnlyToolSet([]string{}) {
		t.Error("expected read-only for empty tools")
	}
}

func TestIsReadOnlyToolSet_WriteNotReadOnly(t *testing.T) {
	if isReadOnlyToolSet([]string{"Read", "Write"}) {
		t.Error("expected not read-only when Write present")
	}
}

func TestIsReadOnlyToolSet_EditNotReadOnly(t *testing.T) {
	if isReadOnlyToolSet([]string{"Read", "Edit"}) {
		t.Error("expected not read-only when Edit present")
	}
}

func TestIsReadOnlyToolSet_BashNotReadOnly(t *testing.T) {
	if isReadOnlyToolSet([]string{"Read", "Bash"}) {
		t.Error("expected not read-only when Bash present")
	}
}

// --- CustomAgent interface compliance ---

func TestCustomAgent_Interface(t *testing.T) {
	pa := &persona.Agent{
		Name:        "devops",
		Description: "Expert DevOps engineer for CI/CD and infrastructure",
		Tools:       persona.StringList{"Read", "Grep", "Glob", "Bash", "Write", "Edit"},
		Skills:      persona.StringList{"clean-code"},
		Content:     "# DevOps Expert\nYou handle deployments and infrastructure.",
	}

	agent := NewCustomAgent(pa, nil)

	// Verify interface
	var _ WorkerAgent = agent

	if agent.Type() != AgentType("devops") {
		t.Errorf("expected type 'devops', got '%s'", agent.Type())
	}
	if agent.Name() != "devops" {
		t.Errorf("expected name 'devops', got '%s'", agent.Name())
	}
	if agent.Description() != "Expert DevOps engineer for CI/CD and infrastructure" {
		t.Errorf("unexpected description: %s", agent.Description())
	}
	if agent.IsReadOnly() {
		t.Error("devops agent with Write/Bash should not be read-only")
	}
	if agent.Skills() == nil {
		t.Error("expected non-nil skills")
	}

	cmds := agent.AllowedCommands()
	if len(cmds) == 0 {
		t.Error("expected non-empty allowed commands")
	}

	// Should contain key commands from tools
	cmdSet := make(map[string]bool)
	for _, c := range cmds {
		cmdSet[c] = true
	}
	for _, expected := range []string{"read", "write", "patch", "exec", "search", "tree", "test", "git-status"} {
		if !cmdSet[expected] {
			t.Errorf("expected command %q in %v", expected, cmds)
		}
	}

	// System prompt should contain agent content
	prompt := agent.SystemPrompt()
	if !strings.Contains(prompt, "DEVOPS") {
		t.Error("expected agent name in system prompt")
	}
	if !strings.Contains(prompt, "DevOps Expert") {
		t.Error("expected agent content in system prompt")
	}
	if !strings.Contains(prompt, "tool_call") {
		t.Error("expected tool_call syntax in system prompt")
	}
}

func TestCustomAgent_ReadOnlyNoTools(t *testing.T) {
	pa := &persona.Agent{
		Name:        "reviewer",
		Description: "Code reviewer",
		Tools:       persona.StringList{},
		Content:     "Review code.",
	}

	agent := NewCustomAgent(pa, nil)

	if !agent.IsReadOnly() {
		t.Error("agent with no tools should be read-only")
	}

	cmds := agent.AllowedCommands()
	if len(cmds) != 3 {
		t.Errorf("expected 3 default commands (read, search, tree), got %d: %v", len(cmds), cmds)
	}

	prompt := agent.SystemPrompt()
	if !strings.Contains(prompt, "READ-ONLY") {
		t.Error("expected READ-ONLY constraint in prompt")
	}
}

func TestCustomAgent_ReadOnlyExplicit(t *testing.T) {
	pa := &persona.Agent{
		Name:  "analyzer",
		Tools: persona.StringList{"Read", "Grep", "Glob"},
	}

	agent := NewCustomAgent(pa, nil)
	if !agent.IsReadOnly() {
		t.Error("agent with only Read/Grep/Glob should be read-only")
	}
}

func TestCustomAgent_WithSkills(t *testing.T) {
	pa := &persona.Agent{
		Name:    "tester",
		Tools:   persona.StringList{"Read", "Bash"},
		Content: "Test expert.",
	}

	skills := []*persona.Skill{
		{
			Name:        "testing-best-practices",
			Description: "Best practices for testing",
			Content:     "Always write unit tests first.",
			Subskills:   map[string]string{"integration.md": "/path/to/integration.md"},
			Scripts:     map[string]string{"scripts/run_tests.sh": "/path/to/run_tests.sh"},
		},
	}

	agent := NewCustomAgent(pa, skills)
	prompt := agent.SystemPrompt()

	if !strings.Contains(prompt, "testing-best-practices") {
		t.Error("expected skill name in system prompt")
	}
	if !strings.Contains(prompt, "Always write unit tests first") {
		t.Error("expected skill content in system prompt")
	}
	if !strings.Contains(prompt, "integration.md") {
		t.Error("expected subskill reference in system prompt")
	}
	if !strings.Contains(prompt, "run_tests.sh") {
		t.Error("expected script reference in system prompt")
	}

	// Check SkillSet
	ss := agent.Skills()
	if _, ok := ss.Get("testing-best-practices"); !ok {
		t.Error("expected descriptive skill registered in SkillSet")
	}
	// Script should be registered as executable
	if _, ok := ss.Get("testing-best-practices/scripts/run_tests.sh"); !ok {
		t.Error("expected executable script skill registered in SkillSet")
	}
}

func TestCustomAgent_Execute(t *testing.T) {
	pa := &persona.Agent{
		Name:    "helper",
		Tools:   persona.StringList{"Read"},
		Content: "You help read files.",
	}

	agent := NewCustomAgent(pa, nil)
	client := &mockLLMClient{responses: []string{"Done helping. Here is the summary."}}
	deps := &WorkerDeps{
		LLMClient: client,
		LockMgr:   NewFileLockManager(),
		Logger:    zap.NewNop(),
	}

	result, err := agent.Execute(context.Background(), "read and summarize main.go", deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Agent != AgentType("helper") {
		t.Errorf("expected agent type 'helper', got '%s'", result.Agent)
	}
	if result.Task != "read and summarize main.go" {
		t.Errorf("expected task preserved, got '%s'", result.Task)
	}
	if result.Output == "" {
		t.Error("expected non-empty output")
	}
}

func TestCustomAgent_NameNormalization(t *testing.T) {
	pa := &persona.Agent{
		Name:  "DevOps-Engineer",
		Tools: persona.StringList{"Read"},
	}

	agent := NewCustomAgent(pa, nil)
	if agent.Type() != AgentType("devops-engineer") {
		t.Errorf("expected lowercase type 'devops-engineer', got '%s'", agent.Type())
	}
}

// --- builtinAgentTypes protection ---

func TestBuiltinAgentTypes_Protected(t *testing.T) {
	for _, builtin := range []AgentType{AgentTypeFile, AgentTypeCoder, AgentTypeShell, AgentTypeGit, AgentTypeSearch, AgentTypePlanner} {
		if !builtinAgentTypes[builtin] {
			t.Errorf("expected %q in builtinAgentTypes", builtin)
		}
	}
}

func TestBuiltinAgentTypes_CustomNotProtected(t *testing.T) {
	for _, custom := range []AgentType{"devops", "security-auditor", "backend-specialist"} {
		if builtinAgentTypes[custom] {
			t.Errorf("%q should NOT be in builtinAgentTypes", custom)
		}
	}
}

// --- sortedKeys helper ---

func TestSortedKeys(t *testing.T) {
	m := map[string]string{"c": "3", "a": "1", "b": "2"}
	keys := sortedKeys(m)
	if len(keys) != 3 || keys[0] != "a" || keys[1] != "b" || keys[2] != "c" {
		t.Errorf("expected sorted keys [a b c], got %v", keys)
	}
}

func TestSortedKeys_Empty(t *testing.T) {
	keys := sortedKeys(map[string]string{})
	if len(keys) != 0 {
		t.Errorf("expected empty keys, got %v", keys)
	}
}
