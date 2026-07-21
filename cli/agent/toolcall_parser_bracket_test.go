package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Claude Opus 4.8 (observado via Bedrock) por vezes emite tool calls no
// shorthand "[tool: @nome {args}]" em vez da tag <tool_call> canônica.
// O parser precisa capturar o formato em vez de obrigar o usuário a
// corrigir o modelo turno a turno (regra do projeto: parsing tolerante).

func TestParseBracketToolCall_JSONArgs(t *testing.T) {
	calls, err := ParseToolCalls(`Vou pesquisar isso.

[tool: @websearch {"query":"golang 1.25 release notes"}]`)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "@websearch", calls[0].Name)
	assert.JSONEq(t, `{"query":"golang 1.25 release notes"}`, calls[0].Args)
}

func TestParseBracketToolCall_MissingAtPrefix(t *testing.T) {
	calls, err := ParseToolCalls(`[tool: websearch {"query":"chatcli"}]`)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "@websearch", calls[0].Name, "bare name must gain the @ prefix")
}

func TestParseBracketToolCall_ToolCallSpelling(t *testing.T) {
	calls, err := ParseToolCalls(`[tool_call: @tools {"cmd":"list"}]`)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "@tools", calls[0].Name)
}

func TestParseBracketToolCall_SingleQuoteRecovery(t *testing.T) {
	// Args maltratados (aspas simples) passam pela recovery normal de JSON.
	calls, err := ParseToolCalls(`[tool: @websearch {'query':'golang'}]`)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	var obj map[string]any
	require.NoError(t, json.Unmarshal([]byte(calls[0].Args), &obj),
		"args must be valid JSON after recovery, got: %s", calls[0].Args)
	assert.Equal(t, "golang", obj["query"])
}

func TestParseBracketToolCall_FlatStringArgs(t *testing.T) {
	// Args sem chaves JSON: o flattener downstream entende "--flag value".
	calls, err := ParseToolCalls(`[tool: @coder read --file main.go]`)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "@coder", calls[0].Name)
	assert.Equal(t, "read --file main.go", calls[0].Args)
}

func TestParseBracketToolCall_CoderExecShape(t *testing.T) {
	// Shape real observado com Opus 4.8 via Bedrock em modo coder.
	calls, err := ParseToolCalls(`[tool: @coder exec ls -t meuarquivo.txt]`)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "@coder", calls[0].Name)
	assert.Equal(t, "exec ls -t meuarquivo.txt", calls[0].Args)
}

func TestParseBracketToolCall_NoArgs(t *testing.T) {
	calls, err := ParseToolCalls(`[tool: @tools]`)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "@tools", calls[0].Name)
	assert.Empty(t, calls[0].Args)
}

func TestParseBracketToolCall_MultipleInBatch(t *testing.T) {
	calls, err := ParseToolCalls(`Preciso de duas coisas:
[tool: @websearch {"query":"a"}]
[tool: @wikipedia {"cmd":"summary","args":{"title":"Go"}}]`)
	require.NoError(t, err)
	require.Len(t, calls, 2)
	assert.Equal(t, "@websearch", calls[0].Name)
	assert.Equal(t, "@wikipedia", calls[1].Name)
}

func TestParseBracketToolCall_UnclosedBracketLenient(t *testing.T) {
	// Resposta truncada: o objeto JSON completo ainda é recuperável.
	calls, err := ParseToolCalls(`[tool: @websearch {"query":"golang"}`)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.JSONEq(t, `{"query":"golang"}`, calls[0].Args)
}

func TestParseBracketToolCall_XMLTakesPrecedence(t *testing.T) {
	// Quando o formato canônico está presente, o shorthand em prose não
	// gera chamadas duplicadas — o estágio bracket é só fallback.
	calls, err := ParseToolCalls(`Como pedido em [tool: syntax], vou usar:
<tool_call name="@websearch" args='{"query":"x"}' />`)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "@websearch", calls[0].Name)
}

func TestParseBracketToolCall_NegativeCases(t *testing.T) {
	for _, text := range []string{
		"a [toolbox: hammer] is not a tool call",
		"see the [tool docs] for details",
		"empty [tool: ] name",
		"no brackets at all",
	} {
		calls, err := ParseToolCalls(text)
		assert.NoError(t, err, text)
		assert.Empty(t, calls, "must not parse a call from: %s", text)
	}
}
