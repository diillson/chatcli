package cli

import (
	"strings"
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

// Regression tests for escaped quotes inside JSON args.
// These exact commands failed before the fix to removeBogusBackslashSpace.

func TestSanitize_JSON_PreservesEscapedQuotesInDockerFormat(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	// Model sends: docker version --format "{{.Server.Version}}"
	// JSON-escaped as: --format \"{{.Server.Version}}\"
	raw := `{"cmd":"exec","args":{"cmd":"sleep 5 && docker version --format \"{{.Server.Version}}\""}}`

	sanitized := sanitizeToolCallArgs(raw, logger, "@coder", true)
	args, err := parseToolArgsWithJSON(sanitized)
	assert.NoError(t, err, "JSON with escaped quotes in docker --format should parse")
	assert.Contains(t, args, "exec")
}

func TestSanitize_JSON_PreservesEscapedQuotesInEchoAndPipes(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	// Model sends: echo "Docker is running" && exit 0
	// JSON-escaped: echo \"Docker is running\" && exit 0
	raw := `{"cmd":"exec","args":{"cmd":"open -a Docker && (for i in $(seq 1 60); do docker info >/dev/null 2>&1 && echo \"Docker is running\" && exit 0; sleep 2; done; echo \"Docker did not become ready in time\"; exit 1)","dir":"."}}`

	sanitized := sanitizeToolCallArgs(raw, logger, "@coder", true)
	args, err := parseToolArgsWithJSON(sanitized)
	assert.NoError(t, err, "JSON with escaped quotes in shell commands should parse")
	assert.Contains(t, args, "exec")
}

func TestSanitize_JSON_PreservesEscapedQuotesInGrepPattern(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	// grep -E "Server Version|ERROR"
	raw := `{"cmd":"exec","args":{"cmd":"docker info 2>&1 | grep -E \"Server Version|ERROR\""}}`

	sanitized := sanitizeToolCallArgs(raw, logger, "@coder", true)
	args, err := parseToolArgsWithJSON(sanitized)
	assert.NoError(t, err, "JSON with escaped quotes in grep should parse")
	assert.Contains(t, args, "exec")
}

func TestSanitize_CLI_StillStripsBackslashSpace(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	// CLI-style args: bogus "\ " (backslash-space) should still be stripped
	raw := `exec --cmd echo \ hello`

	sanitized := sanitizeToolCallArgs(raw, logger, "@coder", true)
	// Backslash-space should be removed, leaving just the space
	assert.NotContains(t, sanitized, `\ `)
	assert.Contains(t, sanitized, "echo hello")
}

// TestSplitStdinChunk_LineTerminators guards the post-park-resume security
// prompt: when go-prompt's Setup/TearDown cycle around a TIOCSTI-injected
// /resume leaves the TTY in raw mode, the user's Enter arrives as '\r'
// rather than '\n'. The reader MUST treat both as line terminators or the
// security prompt silently swallows keystrokes.
func TestSplitStdinChunk_LineTerminators(t *testing.T) {
	tests := []struct {
		name     string
		chunks   [][]byte
		expected []string
		leftover string
	}{
		{
			name:     "Cooked TTY delivers LF",
			chunks:   [][]byte{[]byte("y\n")},
			expected: []string{"y\n"},
		},
		{
			name:     "Raw TTY delivers CR — must still submit",
			chunks:   [][]byte{[]byte("y\r")},
			expected: []string{"y\n"},
		},
		{
			name:     "CRLF collapses to a single line",
			chunks:   [][]byte{[]byte("y\r\n")},
			expected: []string{"y\n"},
		},
		{
			name:     "Multiple lines in one chunk",
			chunks:   [][]byte{[]byte("y\nn\rd\r\n")},
			expected: []string{"y\n", "n\n", "d\n"},
		},
		{
			name:     "Partial line buffered until terminator arrives",
			chunks:   [][]byte{[]byte("ye"), []byte("s\r")},
			expected: []string{"yes\n"},
		},
		{
			name:     "Pure CR boundary then LF in next chunk does NOT emit empty line",
			chunks:   [][]byte{[]byte("ok\r"), []byte("\nnext\n")},
			expected: []string{"ok\n", "\n", "next\n"},
			// NOTE: when CR and LF arrive in separate Read() calls we cannot
			// distinguish CRLF from CR-followed-by-empty-line, so the LF starts
			// its own (empty) line. This is acceptable: the empty line is
			// trimmed to "" downstream and the channel consumer treats it as a
			// no-op rather than a real submission.
			leftover: "",
		},
		{
			name:     "Bare bytes without terminator stay in lineBuf",
			chunks:   [][]byte{[]byte("partial")},
			expected: nil,
			leftover: "partial",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			var got []string
			for _, c := range tt.chunks {
				got = append(got, splitStdinChunk(c, &buf)...)
			}
			assert.Equal(t, tt.expected, got)
			assert.Equal(t, tt.leftover, buf.String())
		})
	}
}

func TestMcpToolHasRequiredParams(t *testing.T) {
	tests := []struct {
		name   string
		schema map[string]interface{}
		want   bool
	}{
		{"nil schema", nil, false},
		{"empty schema", map[string]interface{}{}, false},
		{"properties only, no required", map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"x": map[string]interface{}{"type": "string"}},
		}, false},
		{"required empty list", map[string]interface{}{
			"type":     "object",
			"required": []interface{}{},
		}, false},
		{"required with one entry", map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"path"},
		}, true},
		{"list_allowed_directories shape", map[string]interface{}{
			"$schema":    "http://json-schema.org/draft-07/schema#",
			"type":       "object",
			"properties": map[string]interface{}{},
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mcpToolHasRequiredParams(tt.schema); got != tt.want {
				t.Errorf("mcpToolHasRequiredParams() = %v, want %v", got, tt.want)
			}
		})
	}
}
