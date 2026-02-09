package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestNormalizeShellLineContinuations(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "Continuação simples fora de aspas",
			input: `write --file api.go \
--encoding base64`,
			expected: `write --file api.go  --encoding base64`,
		},
		{
			name:     "Continuação com espaços antes do \\n",
			input:    "write --file api.go \\\n--encoding base64",
			expected: "write --file api.go  --encoding base64",
		},
		{
			name: "Continuação dentro de aspas duplas (remove, não vira espaço)",
			input: `write --file api.go --content "linha1\
linha2"`,
			expected: `write --file api.go --content "linha1linha2"`,
		},
		{
			name: "Continuação dentro de aspas simples (remove)",
			input: `write --file api.go --content 'linha1\
linha2'`,
			expected: `write --file api.go --content 'linha1linha2'`,
		},
		{
			name:     "Base64 sem quebras (mantém intacto)",
			input:    `write --file api.go --content "cGFja2FnZSBtYWluCg=="`,
			expected: `write --file api.go --content "cGFja2FnZSBtYWluCg=="`,
		},
		{
			name: "Múltiplas continuações",
			input: `write --file api.go \
--encoding base64 \
--content 'base64...'`,
			expected: `write --file api.go  --encoding base64  --content 'base64...'`,
		},
		{
			name:     "Barra literal (não seguida de Enter)",
			input:    `write --file api.go --content "caminho\\arquivo.go"`,
			expected: `write --file api.go --content "caminho\\arquivo.go"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeShellLineContinuations(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSanitizeToolCallArgs_WithLineContinuations(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	// Caso real: IA manda com \ + Enter
	rawArgs := `write --file main.go \
--encoding base64 --content "cGFja2FnZSBtYWluCg=="`

	result := sanitizeToolCallArgs(rawArgs, logger, "@coder", true)

	// Esperado: espaços normalizados (um único espaço) e conteúdo base64 preservado
	expected := `write --file main.go --encoding base64 --content "cGFja2FnZSBtYWluCg=="`

	assert.Equal(t, expected, result)

	// Deve parsear sem erro
	parsed, err := splitToolArgsMultiline(result)
	assert.NoError(t, err)
	assert.Contains(t, parsed, "write")
	assert.Contains(t, parsed, "--file")
	assert.Contains(t, parsed, "main.go")
	assert.Contains(t, parsed, "--content")
	assert.Contains(t, parsed, "cGFja2FnZSBtYWluCg==")
}

func TestDanglingBackslashAfterFlag(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	// Caso 1: --content \ (barra sozinha após flag)
	rawArgs1 := "write --file api.go --content \\ cGFja2FnZSBtYWlu"
	result1 := sanitizeToolCallArgs(rawArgs1, logger, "@coder", true)
	assert.Contains(t, result1, "cGFja2FnZSBtYWlu")
	assert.NotContains(t, result1, "\\")

	// Caso 2: --search \ (barra sozinha após flag)
	rawArgs2 := "patch --file main.go --search \\ aWl"
	result2 := sanitizeToolCallArgs(rawArgs2, logger, "@coder", true)
	assert.Contains(t, result2, "aWl")
	assert.NotContains(t, result2, "\\")

	// Caso 3: --cmd \ (barra sozinha após flag)
	rawArgs3 := "exec --cmd \\ go.test"
	result3 := sanitizeToolCallArgs(rawArgs3, logger, "@coder", true)
	assert.Contains(t, result3, "go.test")
	assert.NotContains(t, result3, "\\")
}

func TestParseToolArgsWithJSON_Object(t *testing.T) {
	args, err := parseToolArgsWithJSON(`{"cmd":"read","args":{"file":"main.go"}}`)
	assert.NoError(t, err)
	assert.Equal(t, []string{"read", "--file", "main.go"}, args)
}

func TestParseToolArgsWithJSON_Array(t *testing.T) {
	args, err := parseToolArgsWithJSON(`["read","--file","main.go"]`)
	assert.NoError(t, err)
	assert.Equal(t, []string{"read", "--file", "main.go"}, args)
}

func TestParseToolArgsWithJSON_EscapedObject(t *testing.T) {
	args, err := parseToolArgsWithJSON(`{\"cmd\":\"write\",\"args\":{\"file\":\"main.go\",\"encoding\":\"base64\"}}`)
	assert.NoError(t, err)
	assert.Equal(t, []string{"write", "--encoding", "base64", "--file", "main.go"}, args)
}

func TestSanitizeAndParse_EscapedExecCommand(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	raw := `{\"cmd\":\"exec\",\"args\":{\"command\":\"mkdir -p testeapi\"}}`
	sanitized := sanitizeToolCallArgs(raw, logger, "@coder", true)
	args, err := parseToolArgsWithJSON(sanitized)
	assert.NoError(t, err)
	assert.Equal(t, []string{"exec", "--cmd", "mkdir -p testeapi"}, args)
}
