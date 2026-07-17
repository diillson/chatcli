/*
 * ChatCLI - <tool> alias coverage for strip/trim surfaces.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */

package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// CLI-backed models (Devin/Codex/Claude Code) shorten <tool_call ...> to
// <tool ...>. Every surface that strips or compacts the canonical markup must
// treat the alias identically, or raw tags leak into the rendered chat.

func TestStripToolCallTags_ToolAlias(t *testing.T) {
	in := `before <tool name="@coder" args='{"cmd":"read","args":{"file":"a.go"}}' /> after`
	out := stripToolCallTags(in)
	assert.NotContains(t, out, "<tool")
	assert.Contains(t, out, "before")
	assert.Contains(t, out, "after")
}

func TestStripToolCallTags_ToolAliasPaired(t *testing.T) {
	in := `x <tool name="@websearch" args='{"query":"q"}'>ignored</tool> y`
	out := stripToolCallTags(in)
	assert.NotContains(t, out, "<tool")
	assert.NotContains(t, out, "</tool>")
	assert.Contains(t, out, "x")
	assert.Contains(t, out, "y")
}

func TestStripToolCallTags_CanonicalStillStripped(t *testing.T) {
	in := `a <tool_call name="@coder" args='{"cmd":"tree"}' /> b <tool_call name="@x" args="y">z</tool_call> c`
	out := stripToolCallTags(in)
	assert.NotContains(t, out, "tool_call")
	assert.Contains(t, out, "a")
	assert.Contains(t, out, "b")
	assert.Contains(t, out, "c")
}

func TestStripToolCallTags_ToolboxUntouched(t *testing.T) {
	in := `see the <toolbox> section`
	assert.Equal(t, in, stripToolCallTags(in))
}

func TestCompactToolCalls_ToolAlias(t *testing.T) {
	trimmer := NewMessageTrimmer(zap.NewNop())
	in := `<tool name="@coder" args='{"cmd":"read","args":{"file":"main.go"}}' />`
	out := trimmer.compactToolCalls(in)
	assert.NotContains(t, out, "<tool")
	assert.Contains(t, out, "@coder")
}
