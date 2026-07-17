/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Models backed by other agent CLIs (Devin CLI, Codex CLI, Claude Code)
// frequently shorten the canonical <tool_call ...> tag to <tool ...>. The
// parser must accept both spellings everywhere it accepts the canonical one.

func TestParseToolCalls_ToolAliasSelfClosing(t *testing.T) {
	text := `<reasoning>1. read the file</reasoning>
<tool name="@coder" args='{"cmd":"read","args":{"file":"main.go"}}' />`

	calls, err := ParseToolCalls(text)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "@coder", calls[0].Name)
	assert.JSONEq(t, `{"cmd":"read","args":{"file":"main.go"}}`, calls[0].Args)
	assert.Contains(t, calls[0].Raw, `<tool name=`)
}

func TestParseToolCalls_ToolAliasPaired(t *testing.T) {
	text := `<tool name="@websearch" args='{"query":"golang generics"}'></tool>`

	calls, err := ParseToolCalls(text)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "@websearch", calls[0].Name)
	assert.JSONEq(t, `{"query":"golang generics"}`, calls[0].Args)
	assert.Contains(t, calls[0].Raw, "</tool>")
}

func TestParseToolCalls_MixedTagsKeepDocumentOrder(t *testing.T) {
	text := `<tool_call name="@coder" args='{"cmd":"read","args":{"file":"a.go"}}' />
<tool name="@coder" args='{"cmd":"read","args":{"file":"b.go"}}' />
<tool_call name="@coder" args='{"cmd":"read","args":{"file":"c.go"}}' />`

	calls, err := ParseToolCalls(text)
	require.NoError(t, err)
	require.Len(t, calls, 3)
	assert.Contains(t, calls[0].Args, "a.go")
	assert.Contains(t, calls[1].Args, "b.go")
	assert.Contains(t, calls[2].Args, "c.go")
}

func TestParseToolCalls_ToolAliasCaseInsensitive(t *testing.T) {
	text := `<TOOL name="@coder" args='{"cmd":"tree","args":{}}' />`

	calls, err := ParseToolCalls(text)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "@coder", calls[0].Name)
}

func TestParseToolCalls_ToolAliasArgsWithGreaterThan(t *testing.T) {
	text := `<tool name="@coder" args='{"cmd":"exec","args":{"cmd":"grep -c x file > out.txt"}}' />`

	calls, err := ParseToolCalls(text)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Contains(t, calls[0].Args, "> out.txt")
}

func TestParseToolCalls_ToolPrefixedTagsNotMatched(t *testing.T) {
	// Neither <toolbox>, <tools> nor <tool_caller> are tool calls.
	text := `<toolbox name="@coder" args='{}' /> <tools> </tools> <tool_caller name="@coder" args='{}' />`

	calls, err := ParseToolCalls(text)
	require.NoError(t, err)
	assert.Empty(t, calls)
}

func TestParseToolCalls_PairedToolDoesNotSwallowToolCallClosing(t *testing.T) {
	// A paired <tool> immediately followed by a canonical tool_call: the
	// closing </tool> must terminate the first call without consuming the
	// second tag's closing </tool_call>.
	text := `<tool name="@coder" args='{"cmd":"tree","args":{}}'></tool>
<tool_call name="@coder" args='{"cmd":"git-status","args":{}}'></tool_call>`

	calls, err := ParseToolCalls(text)
	require.NoError(t, err)
	require.Len(t, calls, 2)
	assert.Contains(t, calls[0].Args, "tree")
	assert.Contains(t, calls[1].Args, "git-status")
}
