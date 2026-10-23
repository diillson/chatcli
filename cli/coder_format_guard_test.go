/*
 * ChatCLI - coder format guard tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */

package cli

import (
	"strings"
	"testing"
)

func TestLooksLikeLooseCode_AgentCallWithHashLinesExempt(t *testing.T) {
	// Real-world shape: squad dispatch whose task embeds Python file contents,
	// including comment lines starting with "# " at column zero. The old
	// inline guard matched ^[$#]\s+ against the whole response and rejected
	// the dispatch with a FORMAT ERROR before it was ever parsed.
	resp := `<agent_call agent="coder" task="Crie os arquivos abaixo.

ARQUIVO app/core/init.py - conteudo:

# core
ARQUIVO app/services/init.py - conteudo:

# services" />`
	if looksLikeLooseCode(resp) {
		t.Fatal("agent_call payload with hash-comment lines must not be flagged as loose code")
	}
}

func TestLooksLikeLooseCode_AgentCallWithFenceExempt(t *testing.T) {
	resp := "<agent_call agent=\"coder\" task=\"Documente com:\n```go\nfunc main() {}\n```\" />"
	if looksLikeLooseCode(resp) {
		t.Fatal("agent_call payload with a markdown fence must not be flagged as loose code")
	}
}

func TestLooksLikeLooseCode_PlainFenceFlagged(t *testing.T) {
	resp := "Here is the file:\n```go\npackage main\n```"
	if !looksLikeLooseCode(resp) {
		t.Fatal("bare markdown fence without tool or agent calls must be flagged")
	}
}

func TestLooksLikeLooseCode_ShellPromptFlagged(t *testing.T) {
	resp := "Run this:\n$ go build ./...\nthen check the output"
	if !looksLikeLooseCode(resp) {
		t.Fatal("shell-prompt line without tool or agent calls must be flagged")
	}
}

func TestLooksLikeLooseCode_PlainProseClean(t *testing.T) {
	if looksLikeLooseCode("All files created successfully. The API is ready.") {
		t.Fatal("plain prose must not be flagged")
	}
}

func invokeBlock(tool, params string) string {
	return "<" + "invoke name=\"" + tool + "\">\n" + params + "\n</" + "invoke>"
}

func param(name, value string) string {
	return "<" + "parameter name=\"" + name + "\">" + value + "</" + "parameter>"
}

// The shapes a coder-mode response took when function-call markup leaked
// instead of a tool call: a call in the right structure but the wrong format,
// a tool that does not exist, noise between the blocks, a write cut short and
// a degenerate run of closers.
func TestLooksLikeLeakedToolMarkup_ObservedShapes(t *testing.T) {
	closer := "</" + "invoke>"
	cases := map[string]string{
		"valid structure, wrong format": invokeBlock("@coder", param("cmd", "exec")+"\n"+param("args", `{"cmd":"ls trajectories | head"}`)),
		"tool that does not exist":      invokeBlock("bash", param("command", "cd trajectories/abc && jq . trajectory.json")) + "\n_placeholder\n",
		"noise between blocks": invokeBlock("@coder", param("cmd", "exec")) + "\n_placeholder\n\n\n" +
			invokeBlock("@coder", param("cmd", "exec")) + "\n_placeholder",
		"truncated write":    `..."end_state_index": 3}]}` + "\nEOF\"}</" + "parameter>\n" + closer + "\n_placeholder\n",
		"only stray closers": "\n" + strings.Repeat(closer+"\n", 6),
		"namespaced":         "<" + "ns:invoke name=\"@coder\">" + param("cmd", "exec") + "</" + "ns:invoke>",
	}
	for name, resp := range cases {
		if !looksLikeLeakedToolMarkup(resp) {
			t.Errorf("%s: leaked markup not detected in %q", name, resp)
		}
	}
}

// Responses that talk about the format, or use the right one, must not be
// treated as leaks: an explanation inside code, a valid tool call, plain prose
// that mentions the word, and a single stray closer.
func TestLooksLikeLeakedToolMarkup_NotFlagged(t *testing.T) {
	block := invokeBlock("@coder", param("cmd", "exec"))
	cases := map[string]string{
		"plain prose":            "I will invoke the build and then report the parameters it printed.",
		"valid tool call":        `<tool_call name="@coder" args='{"cmd":"exec","args":{"cmd":"ls"}}' />`,
		"fenced explanation":     "The older format looks like this:\n```xml\n" + block + "\n```\nChatCLI uses <tool_call> instead.",
		"inline code mention":    "Do not write `<" + "invoke name=\"x\">` here; use a tool_call tag.",
		"one stray closer":       "done </" + "invoke> end",
		"empty":                  "",
		"final answer with tags": "<plan>\n- [x] 1. Read the file\n</plan>\nThe value is 42.",
	}
	for name, resp := range cases {
		if looksLikeLeakedToolMarkup(resp) {
			t.Errorf("%s: wrongly flagged: %q", name, resp)
		}
	}
}

// The correction tells the model nothing ran, names the tool the markup
// pointed at, and shows the format that executes; it never repeats the markup.
func TestLeakedToolMarkupFeedback(t *testing.T) {
	resp := invokeBlock("bash", param("command", "ls"))
	got := leakedToolMarkupFeedback(resp)
	for _, want := range []string{"FORMAT ERROR", "Nothing was executed", `"bash"`, "<tool_call", `"cmd":"exec"`} {
		if !strings.Contains(got, want) {
			t.Errorf("feedback missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<"+"invoke") || strings.Contains(got, "</"+"invoke") {
		t.Errorf("feedback must not echo the leaked markup:\n%s", got)
	}
	// Without a named tool (a bare run of closers) it still corrects.
	if g := leakedToolMarkupFeedback(strings.Repeat("</"+"invoke>\n", 4)); !strings.Contains(g, "Nothing was executed") || strings.Contains(g, "named") {
		t.Errorf("unexpected feedback for bare closers: %s", g)
	}
}

func TestMaxLeakedMarkupCorrectionsIsBounded(t *testing.T) {
	if maxLeakedMarkupCorrections < 1 || maxLeakedMarkupCorrections > 5 {
		t.Fatalf("the bound must stay small, got %d", maxLeakedMarkupCorrections)
	}
}
