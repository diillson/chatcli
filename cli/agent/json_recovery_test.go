package agent

import (
	"encoding/json"
	"testing"
)

func TestNormalizeToolArgs_ValidJSON(t *testing.T) {
	input := `{"cmd":"read","args":{"file":"main.go"}}`
	result, ok := NormalizeToolArgs("@coder", input)
	if !ok {
		t.Fatal("expected ok=true for valid JSON")
	}
	if result != input {
		t.Errorf("valid JSON should pass through unchanged, got %q", result)
	}
}

func TestNormalizeToolArgs_SingleQuotes(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect map[string]interface{}
	}{
		{
			name:  "simple single-quoted JSON",
			input: `{'cmd':'read','file':'main.go'}`,
			expect: map[string]interface{}{
				"cmd":  "read",
				"file": "main.go",
			},
		},
		{
			name:  "nested single-quoted JSON",
			input: `{'cmd':'read','args':{'file':'main.go'}}`,
			expect: map[string]interface{}{
				"cmd": "read",
				"args": map[string]interface{}{
					"file": "main.go",
				},
			},
		},
		{
			name:  "mixed quotes",
			input: `{'cmd':"read",'file':"main.go"}`,
			expect: map[string]interface{}{
				"cmd":  "read",
				"file": "main.go",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := NormalizeToolArgs("@coder", tt.input)
			if !ok {
				t.Fatalf("expected ok=true, got false for input %q", tt.input)
			}
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(result), &parsed); err != nil {
				t.Fatalf("result is not valid JSON: %v (result=%q)", err, result)
			}
			for k, v := range tt.expect {
				pv, exists := parsed[k]
				if !exists {
					t.Errorf("expected key %q not found in result", k)
					continue
				}
				// For nested maps, just check they exist
				if _, isMap := v.(map[string]interface{}); isMap {
					if _, isMapParsed := pv.(map[string]interface{}); !isMapParsed {
						t.Errorf("expected key %q to be a map, got %T", k, pv)
					}
					continue
				}
				if pv != v {
					t.Errorf("key %q: expected %v, got %v", k, v, pv)
				}
			}
		})
	}
}

func TestNormalizeToolArgs_UnquotedKeys(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name:   "unquoted keys with quoted values",
			input:  `{cmd: "read", file: "main.go"}`,
			expect: "read",
		},
		{
			name:   "unquoted keys and values",
			input:  `{cmd: read, file: main.go}`,
			expect: "read",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := NormalizeToolArgs("@coder", tt.input)
			if !ok {
				t.Fatalf("expected ok=true for input %q", tt.input)
			}
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(result), &parsed); err != nil {
				t.Fatalf("result is not valid JSON: %v (result=%q)", err, result)
			}
			if cmd, _ := parsed["cmd"].(string); cmd != tt.expect {
				t.Errorf("expected cmd=%q, got %q", tt.expect, cmd)
			}
		})
	}
}

func TestNormalizeToolArgs_TrailingCommas(t *testing.T) {
	input := `{"cmd":"read","file":"main.go",}`
	result, ok := NormalizeToolArgs("@coder", input)
	if !ok {
		t.Fatal("expected ok=true for trailing comma JSON")
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v (result=%q)", err, result)
	}
}

func TestWrapPlainStringForTool(t *testing.T) {
	tests := []struct {
		toolName string
		value    string
		wantKey  string
	}{
		{"read_file", "main.go", "file"},
		{"run_command", "ls -la", "cmd"},
		{"search_files", "TODO", "term"},
		{"list_directory", "/src", "dir"},
		{"read", "config.yaml", "file"},
		{"exec", "go build ./...", "cmd"},
		{"Bash", "echo hello", "command"},
		{"Glob", "**/*.go", "pattern"},
	}

	for _, tt := range tests {
		t.Run(tt.toolName, func(t *testing.T) {
			result := WrapPlainStringForTool(tt.toolName, tt.value)
			if result == "" {
				t.Fatalf("expected non-empty result for tool=%q value=%q", tt.toolName, tt.value)
			}
			var parsed map[string]string
			if err := json.Unmarshal([]byte(result), &parsed); err != nil {
				t.Fatalf("result is not valid JSON: %v (result=%q)", err, result)
			}
			if parsed[tt.wantKey] != tt.value {
				t.Errorf("expected %s=%q, got %q", tt.wantKey, tt.value, parsed[tt.wantKey])
			}
		})
	}
}

func TestWrapPlainStringForTool_IgnoresJSON(t *testing.T) {
	result := WrapPlainStringForTool("read_file", `{"file":"main.go"}`)
	if result != "" {
		t.Errorf("expected empty for JSON input, got %q", result)
	}
}

func TestWrapPlainStringForTool_UnknownTool(t *testing.T) {
	result := WrapPlainStringForTool("unknown_tool", "some value")
	if result != "" {
		t.Errorf("expected empty for unknown tool, got %q", result)
	}
}

func TestIsLikelyStructuredObjectLiteral(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{`{cmd: read, file: main.go}`, true},
		{`{cmd: "read"}`, true},
		{`{"cmd": "read"}`, true},
		{`not an object`, false},
		{`{}`, false},
		{`{no-colon-here}`, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isLikelyStructuredObjectLiteral(tt.input)
			if got != tt.want {
				t.Errorf("isLikelyStructuredObjectLiteral(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestFixSingleQuotedJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{"basic", `{'key':'value'}`, true},
		{"nested", `{'a':{'b':'c'}}`, true},
		{"no single quotes", `{"key":"value"}`, false}, // returns empty
		{"not JSON-like", `hello world`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := fixSingleQuotedJSON(tt.input)
			if tt.valid {
				if result == "" {
					t.Fatal("expected non-empty result")
				}
				if !isValidJSON(result) {
					t.Errorf("result is not valid JSON: %q", result)
				}
			}
		})
	}
}

func TestAggressiveJSONFix(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expectKey string
		expectVal string
	}{
		{
			name:      "unquoted everything",
			input:     `{cmd: read, file: main.go}`,
			expectKey: "cmd",
			expectVal: "read",
		},
		{
			name:      "boolean values",
			input:     `{regex: true, term: TODO}`,
			expectKey: "term",
			expectVal: "TODO",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := aggressiveJSONFix(tt.input)
			if result == "" {
				t.Fatal("expected non-empty result")
			}
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(result), &parsed); err != nil {
				t.Fatalf("result not valid JSON: %v (result=%q)", err, result)
			}
			switch v := parsed[tt.expectKey].(type) {
			case string:
				if v != tt.expectVal {
					t.Errorf("expected %s=%q, got %q", tt.expectKey, tt.expectVal, v)
				}
			default:
				// For booleans and others, just verify the key exists
				if _, exists := parsed[tt.expectKey]; !exists {
					t.Errorf("expected key %q not found", tt.expectKey)
				}
			}
		})
	}
}

func TestNormalizeToolArgs_ComplexEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    string
		wantOK   bool
	}{
		{
			name:     "completely empty",
			toolName: "read",
			input:    "",
			wantOK:   true, // returns "{}"
		},
		{
			name:     "plain filename for read",
			toolName: "read",
			input:    "main.go",
			wantOK:   true, // wraps to {"file":"main.go"}
		},
		{
			name:     "shell command for exec",
			toolName: "exec",
			input:    "go build ./...",
			wantOK:   true, // wraps to {"cmd":"go build ./..."}
		},
		{
			name:     "single-quoted with embedded double quotes",
			toolName: "@coder",
			input:    `{'cmd':'exec','args':{'cmd':'echo "hello"'}}`,
			wantOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := NormalizeToolArgs(tt.toolName, tt.input)
			if ok != tt.wantOK {
				t.Errorf("NormalizeToolArgs(%q, %q): ok=%v, want %v (result=%q)",
					tt.toolName, tt.input, ok, tt.wantOK, result)
			}
			if ok {
				if !isValidJSON(result) {
					t.Errorf("result is not valid JSON: %q", result)
				}
			}
		})
	}
}

// Regression: @websearch args must NOT be detected as @coder search tool call.
// The JSON {"cmd":"search","args":{"query":"..."}} is @websearch format,
// not @coder search (which uses "term" not "query").
func TestParseToolCalls_WebsearchNotDetectedAsCoderSearch(t *testing.T) {
	// Simulate LLM output with @websearch XML + its JSON args visible in text
	text := `<tool_call name="@websearch" args='{"cmd":"search","args":{"query":"próximo jogo Barcelona 2025"}}' />`

	calls, err := ParseToolCalls(text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have exactly 1 call: @websearch
	coderCalls := 0
	for _, c := range calls {
		if c.Name == "@coder" {
			coderCalls++
		}
	}
	if coderCalls > 0 {
		t.Errorf("found %d phantom @coder calls — @websearch args should NOT generate @coder search", coderCalls)
	}

	// Verify @websearch was parsed
	found := false
	for _, c := range calls {
		if c.Name == "@websearch" {
			found = true
		}
	}
	if !found {
		t.Error("expected @websearch tool call not found")
	}
}

func TestJsonObjToToolCall_RejectsWebsearchFormat(t *testing.T) {
	// This JSON looks like @coder search but has "query" key = @websearch
	obj := map[string]interface{}{
		"cmd": "search",
		"args": map[string]interface{}{
			"query": "Barcelona game schedule",
		},
	}
	_, ok := jsonObjToToolCall(obj)
	if ok {
		t.Error("should NOT detect websearch-format JSON as @coder tool call")
	}
}

func TestJsonObjToToolCall_AcceptsCoderSearchFormat(t *testing.T) {
	// This JSON is genuine @coder search with "term" key
	obj := map[string]interface{}{
		"cmd": "search",
		"args": map[string]interface{}{
			"term": "func main",
			"dir":  ".",
		},
	}
	tc, ok := jsonObjToToolCall(obj)
	if !ok {
		t.Fatal("should detect coder search format as valid tool call")
	}
	if tc.Name != "@coder" {
		t.Errorf("expected @coder, got %q", tc.Name)
	}
}
