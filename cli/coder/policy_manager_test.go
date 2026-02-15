package coder

import (
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
