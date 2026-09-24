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

// The bug this guards (field, Opus 5.5 in the coder, Sep 24 2026): a batch
// of four tool calls whose last one was a describe of @proc came out as
// FIVE actions. The JSON scanner lifted {"name":"@proc"} out of the
// describe's args attribute and took it for a call to @proc with no args;
// it passed the security gate and the plugin's parser panicked on the
// empty argv. The object inside a parsed tag's args is that tag's, whatever
// tool it names.
func TestParseToolCalls_DescribeArgsNeverBecomeACall(t *testing.T) {
	response := "3. Subir o servidor em background com @proc (não usar `nohup`, porque trava o terminal)\n" +
		`<tool_call name="@coder" args='{"cmd":"exec","args":{"cmd":"node -v && npm -v","dir":"."}}' />` + "\n" +
		`<tool_call name="@coder" args='{"cmd":"write","args":{"file":"minha-api/server.js","encoding":"base64","content":"Y29uc3Q="}}' />` + "\n" +
		`<tool_call name="@coder" args='{"cmd":"exec","args":{"cmd":"node --check minha-api/server.js","dir":"."}}' />` + "\n" +
		`<tool_call name="@tools" args='{"cmd":"describe","args":{"name":"@proc"}}' />` + "\n"
	calls, err := ParseToolCalls(response)
	require.NoError(t, err)
	require.Len(t, calls, 4, "the describe's subject is not a fifth action: %+v", calls)
	for _, c := range calls {
		assert.NotEqual(t, "@proc", c.Name)
		assert.NotEmpty(t, c.Args, "every call in this batch carries args")
	}
	assert.Equal(t, "@tools", calls[3].Name)

	// Same inside an executable fence (the recovery pass).
	fenced := "```xml\n" + `<tool_call name="@tools" args='{"cmd":"describe","args":{"name":"@proc"}}' />` + "\n```\n"
	calls, err = ParseToolCalls(fenced)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "@tools", calls[0].Name)
}

// A bare {"name":"@x"} in prose is data (a listing, a describe target),
// not a call; the name form needs its args beside it, and the explicit
// tool_call/tool keys keep working without any.
func TestParseToolCalls_BareNameObjectIsNotACall(t *testing.T) {
	calls, err := ParseToolCalls(`The registry lists {"name":"@proc"} and {"name":"@browser"} as builtins.`)
	require.NoError(t, err)
	assert.Empty(t, calls)

	calls, err = ParseToolCalls(`{"name":"@websearch","arguments":{"query":"golang generics"}}`)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "@websearch", calls[0].Name)
	assert.Contains(t, calls[0].Args, "golang generics")

	calls, err = ParseToolCalls(`{"name":"@tree","args":{}}`)
	require.NoError(t, err)
	require.Len(t, calls, 1, "an explicit empty args object is still a call")

	calls, err = ParseToolCalls(`{"tool_call":"@tools"}`)
	require.NoError(t, err)
	require.Len(t, calls, 1, "the tool_call key carries the intent on its own")
	assert.Equal(t, "@tools", calls[0].Name)
}

func TestEmbeddedInParsedCall(t *testing.T) {
	outer := ToolCall{Name: "@tools", Args: `{"cmd":"describe","args":{"name":"@proc"}}`, Raw: `<tool_call name="@tools" args='{"cmd":"describe","args":{"name":"@proc"}}' />`}
	assert.True(t, embeddedInParsedCall([]ToolCall{outer}, ToolCall{Name: "@proc", Raw: `{"name":"@proc"}`}))
	assert.False(t, embeddedInParsedCall([]ToolCall{outer}, ToolCall{Name: "@proc", Raw: `{"name":"@other"}`}))
	assert.False(t, embeddedInParsedCall([]ToolCall{outer}, ToolCall{Name: "@proc"}), "no raw, nothing to compare")
	assert.False(t, embeddedInParsedCall([]ToolCall{outer}, outer), "a call is not embedded in itself")
	assert.False(t, embeddedInParsedCall(nil, ToolCall{Raw: "{}"}))
}
