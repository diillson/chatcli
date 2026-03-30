package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRulesLoader_GlobalRules(t *testing.T) {
	globalDir := t.TempDir()
	rulesDir := filepath.Join(globalDir, "rules")
	_ = os.MkdirAll(rulesDir, 0o755)

	// Create a global rule (no paths = applies everywhere)
	_ = os.WriteFile(filepath.Join(rulesDir, "style.md"), []byte("Always use snake_case for Go variables."), 0o644)

	rl := NewRulesLoader(t.TempDir(), globalDir, testLogger())
	result := rl.LoadMatchingRules(nil)

	if !strings.Contains(result, "snake_case") {
		t.Errorf("expected global rule content, got %q", result)
	}
	if !strings.Contains(result, "style") {
		t.Errorf("expected rule name 'style' in output, got %q", result)
	}
}

func TestRulesLoader_PathSpecificRule(t *testing.T) {
	wsDir := t.TempDir()
	rulesDir := filepath.Join(wsDir, ".chatcli", "rules")
	_ = os.MkdirAll(rulesDir, 0o755)

	// Rule that only applies to Go files
	rule := `---
paths: ["*.go", "src/**"]
---

All Go files must have error handling for every returned error.`
	_ = os.WriteFile(filepath.Join(rulesDir, "go-errors.md"), []byte(rule), 0o644)

	rl := NewRulesLoader(wsDir, t.TempDir(), testLogger())

	// Should match when hint contains a .go file
	result := rl.LoadMatchingRules([]string{"main.go"})
	if !strings.Contains(result, "error handling") {
		t.Errorf("expected path-specific rule to match main.go, got %q", result)
	}

	// Should match for src/** paths
	result = rl.LoadMatchingRules([]string{"src/handler.go"})
	if !strings.Contains(result, "error handling") {
		t.Errorf("expected path-specific rule to match src/handler.go, got %q", result)
	}

	// Should NOT match non-Go files
	result = rl.LoadMatchingRules([]string{"readme.txt"})
	if strings.Contains(result, "error handling") {
		t.Errorf("did not expect rule to match readme.txt, got %q", result)
	}
}

func TestRulesLoader_WorkspaceOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	wsDir := t.TempDir()

	globalRulesDir := filepath.Join(globalDir, "rules")
	wsRulesDir := filepath.Join(wsDir, ".chatcli", "rules")
	_ = os.MkdirAll(globalRulesDir, 0o755)
	_ = os.MkdirAll(wsRulesDir, 0o755)

	// Same name, different content
	_ = os.WriteFile(filepath.Join(globalRulesDir, "naming.md"), []byte("Use camelCase."), 0o644)
	_ = os.WriteFile(filepath.Join(wsRulesDir, "naming.md"), []byte("Use snake_case."), 0o644)

	rl := NewRulesLoader(wsDir, globalDir, testLogger())
	result := rl.LoadMatchingRules(nil)

	if !strings.Contains(result, "snake_case") {
		t.Errorf("expected workspace rule to override global, got %q", result)
	}
	if strings.Contains(result, "camelCase") {
		t.Errorf("global rule should be overridden, but found camelCase in %q", result)
	}
}

func TestRulesLoader_EmptyDir(t *testing.T) {
	rl := NewRulesLoader(t.TempDir(), t.TempDir(), testLogger())
	result := rl.LoadMatchingRules(nil)
	if result != "" {
		t.Errorf("expected empty result for no rules, got %q", result)
	}
}

func TestMatchesAnyPath(t *testing.T) {
	tests := []struct {
		pattern  string
		paths    []string
		expected bool
	}{
		{"*.go", []string{"main.go"}, true},
		{"*.go", []string{"readme.md"}, false},
		{"src/**", []string{"src/main.go"}, true},
		{"src/**", []string{"lib/main.go"}, false},
		{"*.ts", []string{"index.ts", "other.go"}, true},
	}

	for _, tt := range tests {
		if got := matchesAnyPath(tt.pattern, tt.paths); got != tt.expected {
			t.Errorf("matchesAnyPath(%q, %v) = %v, want %v", tt.pattern, tt.paths, got, tt.expected)
		}
	}
}
