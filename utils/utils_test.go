package utils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeSensitiveText_TableDriven(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "OpenAI Key",
			input:    "My key is sk-abc123def456ghi789jkl",
			expected: "My key is sk-[REDACTED]",
		},
		{
			name:     "Anthropic Key",
			input:    "Authorization: sk-ant-xyz789abcde1234567890",
			expected: "Authorization: sk-ant-[REDACTED]",
		},
		{
			// CORREÇÃO AQUI: Use uma chave com o formato correto para o teste
			name:     "Google AI Key in URL",
			input:    "URL: https://api.google.com/v1?key=AIzaSyABCDE1234567890abcdefghijklmno",
			expected: "URL: https://api.google.com/v1?key=[REDACTED_GOOGLE_API_KEY]",
		},
		{
			name:     "Bearer Token",
			input:    "Header: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.e30.abc",
			expected: "Header: Bearer [REDACTED]",
		},
		{
			// CORREÇÃO AQUI: Remova o espaço extra após os dois pontos
			name:     "JSON Access Token",
			input:    `{"access_token": "secret_token_value", "user": "test"}`,
			expected: `{"access_token":"[REDACTED]", "user": "test"}`,
		},
		{
			// CORREÇÃO AQUI: Remova o espaço extra após os dois pontos
			name:     "JSON Client Secret",
			input:    `{"client_id": "123", "client_secret": "very_secret"}`,
			expected: `{"client_id": "123", "client_secret":"[REDACTED]"}`,
		},
		{
			name:     "ENV file format",
			input:    "API_KEY=mysecretkey12345",
			expected: "API_KEY=[REDACTED]",
		},
		{
			name:     "No sensitive data",
			input:    "This is a normal sentence.",
			expected: "This is a normal sentence.",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sanitized := SanitizeSensitiveText(tc.input)
			assert.Equal(t, tc.expected, sanitized)
		})
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	const envKey = "CHATCLI_TEST_ENV"
	const defaultValue = "default_value"

	os.Unsetenv(envKey)
	val := GetEnvOrDefault(envKey, defaultValue)
	assert.Equal(t, defaultValue, val, "Should return default value when env is not set")

	expectedValue := "my_custom_value"
	os.Setenv(envKey, expectedValue)
	val = GetEnvOrDefault(envKey, defaultValue)
	assert.Equal(t, expectedValue, val, "Should return env value when set")

	os.Unsetenv(envKey)
}

func TestIsSensitiveEnvKey(t *testing.T) {
	testCases := []struct {
		key      string
		expected bool
	}{
		{"OPENAI_API_KEY", true},
		{"CLIENT_SECRET", true},
		{"MY_APP_TOKEN", true},
		{"PASSWORD_VAR", true},
		{"some_secret_value", true},
		{"USERNAME", false},
		{"LOG_LEVEL", false},
	}

	for _, tc := range testCases {
		t.Run(tc.key, func(t *testing.T) {
			assert.Equal(t, tc.expected, isSensitiveEnvKey(tc.key))
		})
	}
}

func TestExpandPath(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	assert.NoError(t, err)

	testCases := []struct {
		name     string
		input    string
		expected string
		hasError bool
	}{
		{"Just tilde", "~", homeDir, false},
		{"Tilde with path", "~/documents", filepath.Join(homeDir, "documents"), false},
		{"Absolute path", "/etc/hosts", "/etc/hosts", false},
		{"Relative path", "./test", "./test", false},
		{"Unsupported tilde user", "~user/docs", "", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			expanded, err := ExpandPath(tc.input)
			if tc.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expected, expanded)
			}
		})
	}
}
