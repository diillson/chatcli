/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * toolcall_parser_quoted_test.go
 *
 * Quoted-context rules: tool-call syntax the model merely DESCRIBES — inside
 * inline code spans, documentation fences or with placeholder args — must
 * never parse as an executable call, while the historical recovery paths
 * (real call wrapped in a ```xml/```json fence) keep working.
 */
package agent

import (
	"strings"
	"testing"
)

func TestParseToolCalls_InlineCodeSpanIsIllustrative(t *testing.T) {
	text := "Você pode usar `<tool_call name=\"@websearch\" args='{\"query\":\"golang\"}' />` para pesquisar."
	calls, err := ParseToolCalls(text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("inline-code example must not execute, got %d call(s): %+v", len(calls), calls)
	}
}

func TestParseToolCalls_DocumentationFenceIsIllustrative(t *testing.T) {
	for _, lang := range []string{"bash", "text", "markdown", "exemplo"} {
		text := "A ferramenta funciona assim:\n\n```" + lang + "\n" +
			`<tool_call name="@coder" args='{"cmd":"exec","args":{"command":"rm -rf build"}}' />` +
			"\n```\n\nÉ só isso."
		calls, err := ParseToolCalls(text)
		if err != nil {
			t.Fatalf("[%s] unexpected error: %v", lang, err)
		}
		if len(calls) != 0 {
			t.Fatalf("[%s] documentation fence must not execute, got %+v", lang, calls)
		}
	}
}

func TestParseToolCalls_ExecutableFenceStillRecovers(t *testing.T) {
	for _, lang := range []string{"", "xml", "json"} {
		text := "Vou ler o arquivo agora.\n\n```" + lang + "\n" +
			`<tool_call name="@coder" args='{"cmd":"read","args":{"file":"main.go"}}' />` +
			"\n```\n"
		calls, err := ParseToolCalls(text)
		if err != nil {
			t.Fatalf("[%q] unexpected error: %v", lang, err)
		}
		if len(calls) != 1 || calls[0].Name != "@coder" {
			t.Fatalf("[%q] expected the fenced real call to recover, got %+v", lang, calls)
		}
	}
}

func TestParseToolCalls_UnfencedCallWinsOverFencedExample(t *testing.T) {
	text := `<tool_call name="@coder" args='{"cmd":"read","args":{"file":"go.mod"}}' />` + "\n\n" +
		"Depois disso você poderia rodar:\n\n```xml\n" +
		`<tool_call name="@coder" args='{"cmd":"exec","args":{"command":"go test ./..."}}' />` +
		"\n```\n"
	calls, err := ParseToolCalls(text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected only the unfenced call, got %d: %+v", len(calls), calls)
	}
	if !strings.Contains(calls[0].Args, `"read"`) {
		t.Fatalf("wrong call survived: %+v", calls[0])
	}
}

func TestParseToolCalls_PlaceholderArgsRejected(t *testing.T) {
	cases := []string{
		`<tool_call name="@websearch" args="..." />`,
		`<tool_call name="@tools" args='{"cmd":"describe","args":{"name":"..."}}' />`,
		`Use [tool: @websearch {...}] para pesquisar.`,
	}
	for _, text := range cases {
		calls, err := ParseToolCalls(text)
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", text, err)
		}
		if len(calls) != 0 {
			t.Fatalf("%q: placeholder example must not execute, got %+v", text, calls)
		}
	}
}

func TestParseToolCalls_RealCallsStillParseEverywhere(t *testing.T) {
	// Plain unfenced call with prose around it — the bread-and-butter shape.
	text := "Vou pesquisar.\n" +
		`<tool_call name="@websearch" args='{"query":"golang generics"}' />` + "\nAguarde."
	calls, err := ParseToolCalls(text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 || calls[0].Name != "@websearch" {
		t.Fatalf("expected the real call to parse, got %+v", calls)
	}
}

func TestParseToolCalls_ToolsDescribeEchoDoesNotExecute(t *testing.T) {
	// The exact leak the field reported: the model echoes @tools describe
	// output (usage examples) back to the user inside a documentation fence
	// and in inline code, and the parser used to capture it as a run request.
	text := "O @coder aceita estes comandos. Exemplo de uso:\n\n" +
		"```bash\n" +
		`<tool_call name="@coder" args='{"cmd":"exec","args":{"command":"ls -la"}}' />` + "\n" +
		"```\n\n" +
		"Também dá para chamar via `[tool: @coder exec ls -la]` no formato curto.\n"
	calls, err := ParseToolCalls(text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("describe echo must not reach the execution gate, got %+v", calls)
	}
}

func TestMaskQuotedSegments_TildeFenceAndUnterminated(t *testing.T) {
	fences, masked := maskQuotedSegments("~~~python\nprint('<tool_call name=\"@x\" args=\"1\"/>')\n~~~\nfora\n```json\n{\"tool_call\":\"@coder\",\"args\":{\"cmd\":\"read\"}}")
	if len(fences) != 2 {
		t.Fatalf("expected 2 fences (one unterminated), got %d", len(fences))
	}
	if fences[0].info != "python" || fences[1].info != "json" {
		t.Fatalf("unexpected infos: %+v", fences)
	}
	if strings.Contains(masked, "tool_call") {
		t.Fatalf("fenced content leaked into masked text: %q", masked)
	}
	if !strings.Contains(masked, "fora") {
		t.Fatalf("unfenced text must survive masking: %q", masked)
	}
}
