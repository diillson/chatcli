/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * toolcall_parser_json_escapes_test.go
 *
 * Plain-text content inside args='{…}' is only safe when the JSON escapes
 * reach the JSON decoder untouched: the parser must not collapse "\\\\"
 * (a literal backslash in the content) into "\\" (an escape), which is what
 * forced base64 on every multiline write before.
 */
package agent

import (
	"encoding/json"
	"testing"
)

func TestParseToolCalls_JSONEscapesSurviveInSingleQuotedArgs(t *testing.T) {
	// Go source with a regex, a tab, a quoted string and two lines.
	content := "re := regexp.MustCompile(\"\\\\d+\")\n\tfmt.Println(\"it's\", `raw`)\n"
	inner, _ := json.Marshal(map[string]any{"cmd": "write", "args": map[string]any{"file": "x.go", "content": content}})
	text := `<tool_call name="@coder" args='` + string(inner) + `' />` + "\n"

	calls, err := ParseToolCalls(text)
	if err != nil || len(calls) != 1 {
		t.Fatalf("calls=%d err=%v", len(calls), err)
	}
	var env struct {
		Cmd  string `json:"cmd"`
		Args struct {
			File    string `json:"file"`
			Content string `json:"content"`
		} `json:"args"`
	}
	if err := json.Unmarshal([]byte(calls[0].Args), &env); err != nil {
		t.Fatalf("args must stay valid JSON: %v\n%s", err, calls[0].Args)
	}
	if env.Args.Content != content {
		t.Fatalf("content changed in transit:\n got %q\nwant %q", env.Args.Content, content)
	}
}

// The attribute-level escape of the delimiting quote (\') is not JSON and
// is still unescaped, so args='{"q":"it\'s"}' decodes to "it's".
func TestParseToolCalls_EscapedApostropheInJSONArgsIsUnescaped(t *testing.T) {
	text := `<tool_call name="@websearch" args='{"query":"it\'s fine"}' />`
	calls, err := ParseToolCalls(text)
	if err != nil || len(calls) != 1 {
		t.Fatalf("calls=%d err=%v", len(calls), err)
	}
	var got struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(calls[0].Args), &got); err != nil || got.Query != "it's fine" {
		t.Fatalf("query=%q err=%v args=%s", got.Query, err, calls[0].Args)
	}
}

// A backtick span inside args (a Go raw string, Markdown in a file being
// written) is content, not an inline-code quotation of the call: it must
// reach the tool intact, while a call quoted in inline code still does not
// execute.
func TestParseToolCalls_BacktickSpanInsideArgsIsContent(t *testing.T) {
	content := "const tpl = `Hello, {{.Name}}`\n"
	inner, _ := json.Marshal(map[string]any{"cmd": "write", "args": map[string]any{"file": "tpl.go", "content": content}})
	text := "Use `<tool_call name=\"@coder\" args='{\"cmd\":\"read\"}' />` to read, then:\n" +
		`<tool_call name="@coder" args='` + string(inner) + `' />` + "\n"

	calls, err := ParseToolCalls(text)
	if err != nil || len(calls) != 1 {
		t.Fatalf("calls=%d err=%v (%+v)", len(calls), err, calls)
	}
	var env struct {
		Args struct {
			Content string `json:"content"`
		} `json:"args"`
	}
	if err := json.Unmarshal([]byte(calls[0].Args), &env); err != nil || env.Args.Content != content {
		t.Fatalf("content=%q err=%v args=%s", env.Args.Content, err, calls[0].Args)
	}
}
