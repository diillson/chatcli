/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"encoding/json"
	"testing"
)

func TestRepairJSONInvalidEscapes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "windows path with invalid escapes",
			input: `{"file":"C:\Users\builder\deployment.yaml"}`,
			want:  `{"file":"C:\\Users\\builder\\deployment.yaml"}`,
		},
		{
			name:  "valid JSON untouched",
			input: `{"file":"C:\\Users\\x\\f.yaml","n":"a\nb"}`,
			want:  `{"file":"C:\\Users\\x\\f.yaml","n":"a\nb"}`,
		},
		{
			name:  "valid unicode escape preserved",
			input: "{\"s\":\"\\u0041\"}",
			want:  "{\"s\":\"\\u0041\"}",
		},
		{
			name:  "invalid unicode escape doubled",
			input: `{"s":"C:\users"}`,
			want:  `{"s":"C:\\users"}`,
		},
		{
			name:  "trailing backslash at end of input",
			input: `{"s":"x\`,
			want:  `{"s":"x\\`,
		},
		{
			name:  "backslash outside string untouched region",
			input: `{"a":1}`,
			want:  `{"a":1}`,
		},
		{
			// \b and \t are valid JSON escapes, but in a string that also
			// carries an invalid one (\U) the emitter clearly wasn't
			// escaping — every backslash is a path separator.
			name:  "coincidentally-valid escapes in a dirty string are path separators",
			input: `{"file":"C:\Users\builder\temp\deployment.yaml"}`,
			want:  `{"file":"C:\\Users\\builder\\temp\\deployment.yaml"}`,
		},
		{
			// The dirty-string rewrite is per literal: a sibling string with
			// a legitimate \n escape must survive untouched.
			name:  "clean sibling string keeps its valid escapes",
			input: `{"content":"line1\nline2","file":"C:\Users\x"}`,
			want:  `{"content":"line1\nline2","file":"C:\\Users\\x"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RepairJSONInvalidEscapes(tt.input)
			if got != tt.want {
				t.Errorf("RepairJSONInvalidEscapes(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestRepairJSONInvalidEscapes_ParsesAfterRepair proves the end-to-end
// property: an LLM-emitted Windows path that json.Unmarshal rejects becomes
// parseable after repair, with the path intact.
func TestRepairJSONInvalidEscapes_ParsesAfterRepair(t *testing.T) {
	raw := `{"cmd":"write","args":{"file":"C:\Users\builder\deployment.yaml","encoding":"base64"}}`

	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		t.Fatal("fixture must be invalid JSON before repair (red half of the proof)")
	}

	repaired := RepairJSONInvalidEscapes(raw)
	if err := json.Unmarshal([]byte(repaired), &v); err != nil {
		t.Fatalf("repaired JSON must parse: %v", err)
	}
	args, ok := v["args"].(map[string]any)
	if !ok {
		t.Fatalf("args envelope lost: %#v", v)
	}
	if got := args["file"]; got != `C:\Users\builder\deployment.yaml` {
		t.Errorf("file = %q, want the original Windows path", got)
	}
}
