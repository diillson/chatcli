/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty string", "", "''"},
		{"simple word", "hello", "'hello'"},
		{"with spaces", "hello world", "'hello world'"},
		{"with single quote", "it's", "'it'\\''s'"},
		{"with double quotes", `say "hi"`, `'say "hi"'`},
		{"with backticks", "echo `whoami`", "'echo `whoami`'"},
		{"command substitution", "$(rm -rf /)", "'$(rm -rf /)'"},
		{"semicolon injection", "foo; rm -rf /", "'foo; rm -rf /'"},
		{"pipe injection", "foo | cat /etc/passwd", "'foo | cat /etc/passwd'"},
		{"ampersand injection", "foo && evil", "'foo && evil'"},
		{"newline injection", "foo\nbar", "'foo\nbar'"},
		{"tab character", "foo\tbar", "'foo\tbar'"},
		{"path with spaces", "/home/user/my docs/file.txt", "'/home/user/my docs/file.txt'"},
		{"dollar variable", "$HOME/.bashrc", "'$HOME/.bashrc'"},
		{"backslash", "foo\\bar", "'foo\\bar'"},
		{"multiple single quotes", "it's a 'test'", "'it'\\''s a '\\''test'\\'''"},
		{"glob characters", "*.txt", "'*.txt'"},
		{"unicode characters", "hello 世界", "'hello 世界'"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := ShellQuote(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}
