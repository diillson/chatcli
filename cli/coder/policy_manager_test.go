package coder

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestMatchesWithBoundary(t *testing.T) {
	tests := []struct {
		name        string
		fullCommand string
		pattern     string
		expected    bool
	}{
		{"exact match", "@coder read", "@coder read", true},
		{"match with args", "@coder read file.txt", "@coder read", true},
		{"false prefix readlink vs read", "@coder readlink foo", "@coder read", false},
		{"false prefix readsomething", "@coder readsomething", "@coder read", false},
		{"pattern longer than command", "@coder", "@coder read", false},
		{"match with multiple args", "@coder read file.txt --verbose", "@coder read", true},
		{"no match different command", "@coder write file.txt", "@coder read", false},
		{"match tree exactly", "@coder tree", "@coder tree", true},
		{"false prefix treeline vs tree", "@coder treeline", "@coder tree", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := matchesWithBoundary(tc.fullCommand, tc.pattern)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestGetSuggestedPattern_ExecReturnsEmpty(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		args     string
		want     string
	}{
		{"cli exec returns empty", "@coder", "exec --cmd ls", ""},
		{"json exec returns empty", "@coder", `{"cmd":"exec","args":{"cmd":"ls -la"}}`, ""},
		{"EXEC uppercase returns empty", "@coder", "EXEC --cmd pwd", ""},
		{"read still works", "@coder", `{"cmd":"read","args":{"file":"x"}}`, "@coder read"},
		{"write still works", "@coder", "write --file test.go", "@coder write"},
		{"empty args returns toolName", "@coder", "", "@coder"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := GetSuggestedPattern(tc.toolName, tc.args)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPolicyManager_ExecAlwaysAsks(t *testing.T) {
	pm := &PolicyManager{
		Rules: []Rule{
			{Pattern: "@coder exec", Action: ActionAsk},
			{Pattern: "@coder read", Action: ActionAllow},
		},
		logger: zap.NewNop(),
	}

	// exec should ask regardless
	result := pm.Check("@coder", `{"cmd":"exec","args":{"cmd":"ls -la"}}`)
	assert.Equal(t, ActionAsk, result)

	// read should still allow
	result = pm.Check("@coder", `{"cmd":"read","args":{"file":"main.go"}}`)
	assert.Equal(t, ActionAllow, result)
}

func TestAddRule_RejectsEmptyPattern(t *testing.T) {
	pm := &PolicyManager{
		Rules:  []Rule{},
		logger: zap.NewNop(),
	}
	err := pm.AddRule("", ActionAllow)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestAddRule_RejectsLongPattern(t *testing.T) {
	pm := &PolicyManager{
		Rules:  []Rule{},
		logger: zap.NewNop(),
	}
	longPattern := strings.Repeat("x", 501)
	err := pm.AddRule(longPattern, ActionAllow)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "too long")
}

func TestDeleteRule_RoundtripAndIdempotent(t *testing.T) {
	tmp := t.TempDir()
	pm := &PolicyManager{
		Rules:      []Rule{},
		configPath: tmp + "/policy.json",
		logger:     zap.NewNop(),
	}
	// Add two rules, delete one, confirm persistence + snapshot.
	if err := pm.AddRule("@coder exec my-tool", ActionAllow); err != nil {
		t.Fatalf("add allow: %v", err)
	}
	if err := pm.AddRule("@coder exec rm -rf", ActionDeny); err != nil {
		t.Fatalf("add deny: %v", err)
	}
	if n := len(pm.RulesSnapshot()); n != 2 {
		t.Fatalf("expected 2 rules, got %d", n)
	}

	removed, err := pm.DeleteRule("@coder exec my-tool")
	if err != nil {
		t.Fatalf("delete existing: %v", err)
	}
	if !removed {
		t.Fatal("expected removed=true for existing pattern")
	}
	if n := len(pm.RulesSnapshot()); n != 1 {
		t.Fatalf("expected 1 rule after delete, got %d", n)
	}

	// Idempotent delete — same pattern again is a no-op.
	removed, err = pm.DeleteRule("@coder exec my-tool")
	if err != nil {
		t.Fatalf("double-delete: %v", err)
	}
	if removed {
		t.Error("expected removed=false on second delete")
	}

	// Empty pattern rejected.
	if _, err := pm.DeleteRule("   "); err == nil {
		t.Error("expected error on empty pattern")
	}
}

func TestRulesSnapshot_IsCopy(t *testing.T) {
	pm := &PolicyManager{
		Rules:  []Rule{{Pattern: "@coder exec test", Action: ActionAllow}},
		logger: zap.NewNop(),
	}
	snap := pm.RulesSnapshot()
	// Mutating the returned slice must not affect the manager.
	snap[0].Action = ActionDeny
	if pm.Rules[0].Action != ActionAllow {
		t.Error("snapshot aliasing: mutation leaked into PolicyManager")
	}
}

func TestPolicyManager_Check_BoundaryMatching(t *testing.T) {
	logger := zap.NewNop()

	// Create a PolicyManager with test rules (without loading from disk)
	pm := &PolicyManager{
		Rules: []Rule{
			{Pattern: "@coder read", Action: ActionAllow},
			{Pattern: "@coder git-status", Action: ActionAllow},
			{Pattern: "@coder write", Action: ActionDeny},
		},
		logger: logger,
	}

	tests := []struct {
		name     string
		toolName string
		args     string
		expected Action
	}{
		// Boundary matching: "readlink" should NOT match "read" rule
		{"read exact should allow", "@coder", "read file.txt", ActionAllow},
		{"git-status should allow", "@coder", "git-status", ActionAllow},
		{"write should deny", "@coder", "write file.txt content", ActionDeny},
		{"unknown command should ask", "@coder", "delete file.txt", ActionAsk},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := pm.Check(tc.toolName, tc.args)
			assert.Equal(t, tc.expected, result)
		})
	}
}
