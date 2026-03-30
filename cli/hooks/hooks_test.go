package hooks

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"
)

func testLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

func TestMatchToolPattern(t *testing.T) {
	tests := []struct {
		pattern  string
		tool     string
		expected bool
	}{
		{"*", "anything", true},
		{"mcp_*", "mcp_read_file", true},
		{"mcp_*", "@coder", false},
		{"@coder", "@coder", true},
		{"@coder", "@other", false},
	}
	for _, tt := range tests {
		if got := matchToolPattern(tt.pattern, tt.tool); got != tt.expected {
			t.Errorf("matchToolPattern(%q, %q) = %v, want %v", tt.pattern, tt.tool, got, tt.expected)
		}
	}
}

func TestHookConfig_IsEnabled(t *testing.T) {
	h1 := HookConfig{Name: "test"}
	if !h1.IsEnabled() {
		t.Error("expected default enabled")
	}

	f := false
	h2 := HookConfig{Name: "test", Enabled: &f}
	if h2.IsEnabled() {
		t.Error("expected disabled")
	}
}

func TestHookConfig_GetTimeout(t *testing.T) {
	h1 := HookConfig{}
	if h1.GetTimeout() != 10000 {
		t.Errorf("expected default 10000, got %d", h1.GetTimeout())
	}

	h2 := HookConfig{Timeout: 5000}
	if h2.GetTimeout() != 5000 {
		t.Errorf("expected 5000, got %d", h2.GetTimeout())
	}
}

func TestManager_LoadFromFile(t *testing.T) {
	tmpFile := t.TempDir() + "/hooks.json"
	config := HooksConfig{
		Hooks: []HookConfig{
			{Name: "test-hook", Event: EventPreToolUse, Type: HookTypeCommand, Command: "echo hello"},
			{Name: "http-hook", Event: EventPostToolUse, Type: HookTypeHTTP, URL: "http://localhost:9999/hook"},
		},
	}
	data, _ := json.Marshal(config)
	_ = os.WriteFile(tmpFile, data, 0o644)

	m := NewManager(testLogger())
	err := m.LoadFromFile(tmpFile)
	if err != nil {
		t.Fatal(err)
	}

	if m.Count() != 2 {
		t.Errorf("expected 2 hooks, got %d", m.Count())
	}
}

func TestManager_LoadFromFile_NotExists(t *testing.T) {
	m := NewManager(testLogger())
	err := m.LoadFromFile("/nonexistent/hooks.json")
	if err != nil {
		t.Error("expected no error for missing file")
	}
	if m.Count() != 0 {
		t.Errorf("expected 0 hooks, got %d", m.Count())
	}
}

func TestManager_Fire_CommandHook(t *testing.T) {
	m := NewManager(testLogger())
	m.hooks = []HookConfig{
		{
			Name:    "echo-hook",
			Event:   EventPostToolUse,
			Type:    HookTypeCommand,
			Command: "echo hook-ran",
			Timeout: 5000,
		},
	}

	result := m.Fire(HookEvent{
		Type:      EventPostToolUse,
		Timestamp: time.Now(),
		ToolName:  "test-tool",
	})

	// PostToolUse hooks don't block
	if result != nil && result.Blocked {
		t.Error("PostToolUse should not block")
	}
}

func TestManager_Fire_PreToolUse_Block(t *testing.T) {
	m := NewManager(testLogger())
	m.hooks = []HookConfig{
		{
			Name:    "blocker",
			Event:   EventPreToolUse,
			Type:    HookTypeCommand,
			Command: "exit 2",
			Timeout: 5000,
		},
	}

	result := m.Fire(HookEvent{
		Type:      EventPreToolUse,
		Timestamp: time.Now(),
		ToolName:  "dangerous-tool",
	})

	if result == nil {
		t.Fatal("expected blocking result")
	}
	if !result.Blocked {
		t.Error("expected blocked=true for exit code 2")
	}
}

func TestManager_Fire_PreToolUse_Allow(t *testing.T) {
	m := NewManager(testLogger())
	m.hooks = []HookConfig{
		{
			Name:    "allower",
			Event:   EventPreToolUse,
			Type:    HookTypeCommand,
			Command: "exit 0",
			Timeout: 5000,
		},
	}

	result := m.Fire(HookEvent{
		Type:      EventPreToolUse,
		Timestamp: time.Now(),
		ToolName:  "safe-tool",
	})

	if result != nil && result.Blocked {
		t.Error("expected no blocking for exit code 0")
	}
}

func TestManager_Fire_ToolPattern(t *testing.T) {
	m := NewManager(testLogger())
	m.hooks = []HookConfig{
		{
			Name:        "mcp-only",
			Event:       EventPreToolUse,
			Type:        HookTypeCommand,
			Command:     "exit 2",
			ToolPattern: "mcp_*",
			Timeout:     5000,
		},
	}

	// MCP tool should be blocked
	result := m.Fire(HookEvent{
		Type:     EventPreToolUse,
		ToolName: "mcp_read_file",
	})
	if result == nil || !result.Blocked {
		t.Error("expected mcp_read_file to be blocked")
	}

	// Non-MCP tool should pass
	result = m.Fire(HookEvent{
		Type:     EventPreToolUse,
		ToolName: "@coder",
	})
	if result != nil && result.Blocked {
		t.Error("expected @coder to NOT be blocked by mcp_* pattern")
	}
}

func TestManager_Fire_DisabledHook(t *testing.T) {
	f := false
	m := NewManager(testLogger())
	m.hooks = []HookConfig{
		{
			Name:    "disabled",
			Event:   EventPreToolUse,
			Type:    HookTypeCommand,
			Command: "exit 2",
			Enabled: &f,
		},
	}

	result := m.Fire(HookEvent{
		Type:     EventPreToolUse,
		ToolName: "any-tool",
	})
	if result != nil && result.Blocked {
		t.Error("disabled hook should not block")
	}
}

func TestManager_Fire_EventMismatch(t *testing.T) {
	m := NewManager(testLogger())
	m.hooks = []HookConfig{
		{
			Name:    "session-only",
			Event:   EventSessionStart,
			Type:    HookTypeCommand,
			Command: "exit 2",
		},
	}

	result := m.Fire(HookEvent{
		Type:     EventPreToolUse,
		ToolName: "any-tool",
	})
	if result != nil && result.Blocked {
		t.Error("hook for different event should not fire")
	}
}
