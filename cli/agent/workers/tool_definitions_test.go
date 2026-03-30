package workers

import (
	"testing"
)

func TestCoderToolDefinitions_AllTools(t *testing.T) {
	defs := CoderToolDefinitions(nil)
	if len(defs) == 0 {
		t.Fatal("expected non-empty tool definitions")
	}

	// Verify all tools have proper structure
	for _, td := range defs {
		if td.Type != "function" {
			t.Errorf("tool %s: expected type 'function', got %q", td.Function.Name, td.Type)
		}
		if td.Function.Name == "" {
			t.Error("tool has empty name")
		}
		if td.Function.Description == "" {
			t.Errorf("tool %s has empty description", td.Function.Name)
		}
		if td.Function.Parameters == nil {
			t.Errorf("tool %s has nil parameters", td.Function.Name)
		}
	}
}

func TestCoderToolDefinitions_FilteredByAllowed(t *testing.T) {
	defs := CoderToolDefinitions([]string{"read", "write"})
	if len(defs) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(defs))
	}

	names := map[string]bool{}
	for _, td := range defs {
		names[td.Function.Name] = true
	}
	if !names["read_file"] {
		t.Error("expected read_file tool")
	}
	if !names["write_file"] {
		t.Error("expected write_file tool")
	}
}

func TestNativeToolNameToSubcmd(t *testing.T) {
	tests := []struct {
		name    string
		want    string
		isKnown bool
	}{
		{"read_file", "read", true},
		{"write_file", "write", true},
		{"patch_file", "patch", true},
		{"run_command", "exec", true},
		{"list_directory", "tree", true},
		{"search_files", "search", true},
		{"git_status", "git-status", true},
		{"run_tests", "test", true},
		{"unknown_tool", "unknown_tool", false},
	}

	for _, tt := range tests {
		got, known := NativeToolNameToSubcmd(tt.name)
		if got != tt.want {
			t.Errorf("NativeToolNameToSubcmd(%q) = %q, want %q", tt.name, got, tt.want)
		}
		if known != tt.isKnown {
			t.Errorf("NativeToolNameToSubcmd(%q) known=%v, want %v", tt.name, known, tt.isKnown)
		}
	}
}

func TestNativeToolArgsToFlags(t *testing.T) {
	tests := []struct {
		subcmd string
		args   map[string]interface{}
		check  func([]string) bool
		desc   string
	}{
		{
			subcmd: "read",
			args:   map[string]interface{}{"file": "main.go", "start": float64(10)},
			check: func(flags []string) bool {
				return containsFlag(flags, "--file", "main.go") && containsFlag(flags, "--start", "10")
			},
			desc: "read with file and start line",
		},
		{
			subcmd: "write",
			args:   map[string]interface{}{"file": "out.go", "content": "package main\n"},
			check: func(flags []string) bool {
				return containsFlag(flags, "--file", "out.go") && containsFlag(flags, "--encoding", "text")
			},
			desc: "write defaults to text encoding",
		},
		{
			subcmd: "exec",
			args:   map[string]interface{}{"cmd": "go test ./..."},
			check: func(flags []string) bool {
				return containsFlag(flags, "--cmd", "go test ./...")
			},
			desc: "exec with command",
		},
		{
			subcmd: "tree",
			args:   map[string]interface{}{"max_depth": float64(3), "include_hidden": true},
			check: func(flags []string) bool {
				return containsFlag(flags, "--max-depth", "3") && containsFlagBool(flags, "--include-hidden")
			},
			desc: "tree with depth and hidden",
		},
	}

	for _, tt := range tests {
		flags := NativeToolArgsToFlags(tt.subcmd, tt.args)
		if !tt.check(flags) {
			t.Errorf("%s: flags=%v did not pass check", tt.desc, flags)
		}
	}
}

func containsFlag(flags []string, key, val string) bool {
	for i, f := range flags {
		if f == key && i+1 < len(flags) && flags[i+1] == val {
			return true
		}
	}
	return false
}

func containsFlagBool(flags []string, key string) bool {
	for _, f := range flags {
		if f == key {
			return true
		}
	}
	return false
}
